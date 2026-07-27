package db

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/app/config"
)

// RolePrivilege describes PostgreSQL role flags relevant to production hardening.
type RolePrivilege struct {
	Name            string `json:"name"`
	Superuser       bool   `json:"superuser"`
	CreateDB        bool   `json:"create_db"`
	CreateRole      bool   `json:"create_role"`
	Replication     bool   `json:"replication"`
	BypassRLS       bool   `json:"bypass_rls"`
	Inherit         bool   `json:"inherit"`
	PrivilegedFlags bool   `json:"privileged_flags"`
}

// ReadonlyProbeResult captures write/DDL/catalog/app-schema checks for the readonly role.
type ReadonlyProbeResult struct {
	ConnectionID   string `json:"connection_id,omitempty"`
	WriteBlocked   bool   `json:"write_blocked"`
	DDLBlocked     bool   `json:"ddl_blocked"`
	CatalogBlocked bool   `json:"catalog_blocked"`
	AppBlocked     bool   `json:"app_blocked"`
	Error          string `json:"error,omitempty"`
}

// SecurityAuditReport summarizes database privilege posture for operators.
type SecurityAuditReport struct {
	CheckedAt      time.Time             `json:"checked_at"`
	AppRole        string                `json:"app_role"`
	ReadonlyRole   string                `json:"readonly_role"`
	Roles          []RolePrivilege       `json:"roles"`
	ReadonlyProbes []ReadonlyProbeResult `json:"readonly_probes"`
	OK             bool                  `json:"ok"`
	Issues         []string              `json:"issues,omitempty"`
}

// AuditSecurityBoundary inspects configured app/readonly roles and probes readonly restrictions
// for every configured analytical connection.
func AuditSecurityBoundary(ctx context.Context, appPool *pgxpool.Pool, cfg config.DatabaseConfig) SecurityAuditReport {
	report := SecurityAuditReport{
		CheckedAt:    time.Now().UTC(),
		AppRole:      cfg.User,
		ReadonlyRole: cfg.ReadOnlyUser,
		OK:           true,
	}
	if appPool == nil {
		report.OK = false
		report.Issues = append(report.Issues, "app pool not configured")
		return report
	}
	roles, roleIssues := loadRolePrivileges(ctx, appPool, cfg.User, cfg.ReadOnlyUser)
	report.Roles = roles
	report.Issues = append(report.Issues, roleIssues...)

	conns := cfg.Connections
	if len(conns) == 0 {
		conns = []config.DataConnectionConfig{{
			ID:               "default",
			Host:             cfg.Host,
			Port:             cfg.Port,
			Database:         cfg.Database,
			ReadOnlyUser:     cfg.ReadOnlyUser,
			ReadOnlyPassword: cfg.ReadOnlyPassword,
			SSLMode:          cfg.SSLMode,
			AllowedSchemas:   append([]string(nil), cfg.AllowedSchemas...),
		}}
	}
	for _, conn := range conns {
		probes, probeIssues := probeReadonlyConnection(ctx, conn, cfg.AllowedSchemas)
		report.ReadonlyProbes = append(report.ReadonlyProbes, probes)
		report.Issues = append(report.Issues, probeIssues...)
	}

	if len(report.Issues) > 0 {
		report.OK = false
	}
	return report
}

// AuditSecurityBoundaryWithSecrets extends AuditSecurityBoundary by verifying decryptable tenant DSNs.
func AuditSecurityBoundaryWithSecrets(ctx context.Context, appPool *pgxpool.Pool, cfg config.DatabaseConfig, encKey string) SecurityAuditReport {
	report := AuditSecurityBoundary(ctx, appPool, cfg)
	n := int64(0)
	if secrets := auth.NewOrgConnectionSecretStore(appPool, encKey); secrets != nil {
		if count, err := secrets.CountSecrets(ctx); err == nil {
			n = count
		}
	}
	if n > 0 && strings.TrimSpace(encKey) == "" {
		report.Issues = append(report.Issues, "SECURITY_DATA_ENCRYPTION_KEY is required to verify organisation connection secrets")
		report.OK = false
		return report
	}
	store := auth.NewOrgConnectionSecretStore(appPool, encKey)
	if store == nil || strings.TrimSpace(encKey) == "" {
		return report
	}
	metas, err := listAllConnectionSecrets(ctx, appPool)
	if err != nil {
		report.Issues = append(report.Issues, "failed to list organisation connection secrets: "+err.Error())
		report.OK = false
		return report
	}
	for _, meta := range metas {
		res, err := store.Resolve(ctx, meta.orgID, meta.connectionID)
		if err != nil {
			report.Issues = append(report.Issues, fmt.Sprintf("org %s conn %s resolve failed: %v", meta.orgID, meta.connectionID, err))
			continue
		}
		if res.FailClosed() {
			report.Issues = append(report.Issues, fmt.Sprintf("org %s conn %s: %v", meta.orgID, meta.connectionID, res.Error()))
			continue
		}
		if res.Mode != auth.OrgConnectionDedicated {
			continue
		}
		if err := VerifyTenantDSNWithOptions(ctx, res.DSN, TenantDSNVerifyOptions{
			RequireTLS:     config.StrictMode(),
			AllowedSchemas: res.Schemas,
		}); err != nil {
			report.Issues = append(report.Issues, fmt.Sprintf("org %s conn %s DSN verification failed: %v", meta.orgID, meta.connectionID, err))
		}
	}
	if len(report.Issues) > 0 {
		report.OK = false
	}
	return report
}

type secretMetaRef struct {
	orgID        string
	connectionID string
}

func listAllConnectionSecrets(ctx context.Context, pool *pgxpool.Pool) ([]secretMetaRef, error) {
	rows, err := pool.Query(ctx, `
		SELECT organization_id::text, connection_id
		FROM app.list_organization_connection_secrets()
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []secretMetaRef
	for rows.Next() {
		var m secretMetaRef
		if err := rows.Scan(&m.orgID, &m.connectionID); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func loadRolePrivileges(ctx context.Context, pool *pgxpool.Pool, names ...string) ([]RolePrivilege, []string) {
	var out []RolePrivilege
	var issues []string
	rows, err := pool.Query(ctx, `
		SELECT rolname, rolsuper, rolcreatedb, rolcreaterole, rolreplication, rolbypassrls, rolinherit
		FROM pg_roles
		WHERE rolname = ANY($1)
		ORDER BY rolname
	`, names)
	if err != nil {
		issues = append(issues, fmt.Sprintf("role catalog query failed: %v", err))
		return out, issues
	}
	defer rows.Close()
	for rows.Next() {
		var rp RolePrivilege
		if err := rows.Scan(&rp.Name, &rp.Superuser, &rp.CreateDB, &rp.CreateRole, &rp.Replication, &rp.BypassRLS, &rp.Inherit); err != nil {
			issues = append(issues, fmt.Sprintf("role scan failed: %v", err))
			continue
		}
		rp.PrivilegedFlags = rp.Superuser || rp.CreateDB || rp.CreateRole || rp.Replication || rp.BypassRLS
		if rp.PrivilegedFlags {
			issues = append(issues, fmt.Sprintf("role %s has privileged flags enabled", rp.Name))
		}
		out = append(out, rp)
	}
	if err := rows.Err(); err != nil {
		issues = append(issues, fmt.Sprintf("role rows error: %v", err))
	}
	return out, issues
}

func probeReadonlyConnection(ctx context.Context, conn config.DataConnectionConfig, fallbackSchemas []string) (ReadonlyProbeResult, []string) {
	id := conn.ID
	if id == "" {
		id = "default"
	}
	result := ReadonlyProbeResult{
		ConnectionID:   id,
		WriteBlocked:   true,
		DDLBlocked:     true,
		CatalogBlocked: true,
		AppBlocked:     true,
	}
	var issues []string

	schema := "demo"
	schemas := conn.AllowedSchemas
	if len(schemas) == 0 {
		schemas = fallbackSchemas
	}
	if len(schemas) > 0 {
		schema = schemas[0]
	}
	readURL := buildConnectionURL(conn.ReadOnlyUser, conn.ReadOnlyPassword, conn.Host, conn.Port, conn.Database, conn.SSLMode)
	pool, err := newPoolWithRetries(ctx, readURL, poolOptions{connectTimeout: 5 * time.Second})
	if err != nil {
		result.Error = err.Error()
		issues = append(issues, fmt.Sprintf("readonly probe connection %s failed: %s", id, err.Error()))
		return result, issues
	}
	defer pool.Close()

	prefix := fmt.Sprintf("connection %s: ", id)
	if _, err := pool.Exec(ctx, fmt.Sprintf("INSERT INTO %s.sales DEFAULT VALUES", pgxQuoteIdent(schema))); err == nil {
		result.WriteBlocked = false
		issues = append(issues, prefix+"readonly role can write to allowed schema")
	} else if !isPermissionDenied(err) && !isUndefinedTable(err) {
		result.WriteBlocked = false
		issues = append(issues, prefix+"readonly write probe inconclusive: "+err.Error())
	}

	if _, err := pool.Exec(ctx, fmt.Sprintf("CREATE TABLE %s.pgqn_forbidden_write(id int)", pgxQuoteIdent(schema))); err == nil {
		result.DDLBlocked = false
		issues = append(issues, prefix+"readonly role can create tables")
	} else if !isPermissionDenied(err) {
		result.DDLBlocked = false
		issues = append(issues, prefix+"readonly DDL probe inconclusive: "+err.Error())
	}

	if err := pool.QueryRow(ctx, "SELECT 1 FROM pg_catalog.pg_authid LIMIT 1").Scan(new(int)); err == nil {
		result.CatalogBlocked = false
		issues = append(issues, prefix+"readonly role can read pg_authid")
	} else if !isPermissionDenied(err) {
		result.CatalogBlocked = false
		issues = append(issues, prefix+"readonly catalog probe inconclusive: "+err.Error())
	}

	if err := pool.QueryRow(ctx, "SELECT 1 FROM app.saved_queries LIMIT 1").Scan(new(int)); err == nil {
		result.AppBlocked = false
		issues = append(issues, prefix+"readonly role can read app.saved_queries")
	} else if !isPermissionDenied(err) && !isUndefinedTable(err) {
		result.AppBlocked = false
		issues = append(issues, prefix+"readonly app-schema probe inconclusive: "+err.Error())
	}
	var hasUsage bool
	if err := pool.QueryRow(ctx, `SELECT has_schema_privilege(current_user, 'app', 'USAGE')`).Scan(&hasUsage); err == nil && hasUsage {
		result.AppBlocked = false
		issues = append(issues, prefix+"readonly role has USAGE on app schema")
	}

	return result, issues
}

func isPermissionDenied(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "permission denied") ||
		strings.Contains(msg, "insufficient privilege") ||
		strings.Contains(msg, "must be owner")
}

func isUndefinedTable(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "does not exist") || strings.Contains(msg, "undefined table")
}

func pgxQuoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
