package db

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

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

// ReadonlyProbeResult captures write/DDL/catalog checks for the readonly role.
type ReadonlyProbeResult struct {
	WriteBlocked   bool   `json:"write_blocked"`
	DDLBlocked     bool   `json:"ddl_blocked"`
	CatalogBlocked bool   `json:"catalog_blocked"`
	Error          string `json:"error,omitempty"`
}

// SecurityAuditReport summarizes database privilege posture for operators.
type SecurityAuditReport struct {
	CheckedAt      time.Time           `json:"checked_at"`
	AppRole        string              `json:"app_role"`
	ReadonlyRole   string              `json:"readonly_role"`
	Roles          []RolePrivilege     `json:"roles"`
	ReadonlyProbes ReadonlyProbeResult `json:"readonly_probes"`
	OK             bool                `json:"ok"`
	Issues         []string            `json:"issues,omitempty"`
}

// AuditSecurityBoundary inspects configured app/readonly roles and probes readonly restrictions.
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
	probes, probeIssues := probeReadonlyRole(ctx, cfg)
	report.ReadonlyProbes = probes
	report.Issues = append(report.Issues, probeIssues...)
	if len(report.Issues) > 0 {
		report.OK = false
	}
	return report
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

func probeReadonlyRole(ctx context.Context, cfg config.DatabaseConfig) (ReadonlyProbeResult, []string) {
	result := ReadonlyProbeResult{WriteBlocked: true, DDLBlocked: true, CatalogBlocked: true}
	var issues []string

	conn := cfg.Connections
	if len(conn) == 0 {
		conn = []config.DataConnectionConfig{{
			Host:             cfg.Host,
			Port:             cfg.Port,
			Database:         cfg.Database,
			ReadOnlyUser:     cfg.ReadOnlyUser,
			ReadOnlyPassword: cfg.ReadOnlyPassword,
			SSLMode:          cfg.SSLMode,
			AllowedSchemas:   append([]string(nil), cfg.AllowedSchemas...),
		}}
	}
	first := conn[0]
	schema := "demo"
	if len(first.AllowedSchemas) > 0 {
		schema = first.AllowedSchemas[0]
	}
	readURL := buildConnectionURL(first.ReadOnlyUser, first.ReadOnlyPassword, first.Host, first.Port, first.Database, first.SSLMode)
	pool, err := newPoolWithRetries(ctx, readURL, poolOptions{connectTimeout: 5 * time.Second})
	if err != nil {
		result.Error = err.Error()
		issues = append(issues, "readonly probe connection failed: "+err.Error())
		return result, issues
	}
	defer pool.Close()

	if _, err := pool.Exec(ctx, fmt.Sprintf("INSERT INTO %s.sales DEFAULT VALUES", pgxQuoteIdent(schema))); err == nil {
		result.WriteBlocked = false
		issues = append(issues, "readonly role can write to allowed schema")
	} else if !isPermissionDenied(err) {
		result.WriteBlocked = false
		issues = append(issues, "readonly write probe inconclusive: "+err.Error())
	}

	if _, err := pool.Exec(ctx, fmt.Sprintf("CREATE TABLE %s.pgqn_forbidden_write(id int)", pgxQuoteIdent(schema))); err == nil {
		result.DDLBlocked = false
		issues = append(issues, "readonly role can create tables")
	} else if !isPermissionDenied(err) {
		result.DDLBlocked = false
		issues = append(issues, "readonly DDL probe inconclusive: "+err.Error())
	}

	if err := pool.QueryRow(ctx, "SELECT 1 FROM pg_catalog.pg_authid LIMIT 1").Scan(new(int)); err == nil {
		result.CatalogBlocked = false
		issues = append(issues, "readonly role can read pg_authid")
	} else if !isPermissionDenied(err) {
		result.CatalogBlocked = false
		issues = append(issues, "readonly catalog probe inconclusive: "+err.Error())
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

func pgxQuoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
