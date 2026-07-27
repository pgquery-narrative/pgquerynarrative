package db

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/app/config"
)

// dangerousPredefinedRoles are PostgreSQL predefined roles that must not be granted
// to tenant analytics credentials.
var dangerousPredefinedRoles = []string{
	"pg_read_server_files",
	"pg_write_server_files",
	"pg_execute_server_program",
	"pg_read_all_data",
	"pg_write_all_data",
}

// TenantDSNVerifyOptions controls VerifyTenantDSNWithOptions behaviour.
type TenantDSNVerifyOptions struct {
	// RequireTLS rejects DSNs that do not use require/verify-ca/verify-full.
	// Defaults to true when unset via VerifyTenantDSN.
	RequireTLS bool
	// AllowedSchemas, when non-nil, must pass config.ValidateTenantAllowedSchemas.
	AllowedSchemas []string
	// HostAllowlist, when non-empty, requires the DSN host to match one entry
	// (exact hostname or CIDR containing the resolved IP).
	HostAllowlist []string
}

// VerifyTenantDSN rejects tenant analytics DSNs that are not safely read-only.
// TLS is always required on this path (admin onboarding).
func VerifyTenantDSN(ctx context.Context, dsn string) error {
	return VerifyTenantDSNWithOptions(ctx, dsn, TenantDSNVerifyOptions{RequireTLS: true})
}

// VerifyTenantDSNWithOptions runs configurable tenant DSN security checks.
func VerifyTenantDSNWithOptions(ctx context.Context, dsn string, opts TenantDSNVerifyOptions) error {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return fmt.Errorf("tenant DSN is required")
	}
	if opts.RequireTLS && !dsnUsesTLS(dsn) {
		return fmt.Errorf("tenant DSN must require TLS")
	}
	if opts.AllowedSchemas != nil {
		if err := config.ValidateTenantAllowedSchemas(opts.AllowedSchemas); err != nil {
			return err
		}
	}
	if len(opts.HostAllowlist) > 0 {
		if err := verifyTenantDSNHost(dsn, opts.HostAllowlist); err != nil {
			return err
		}
	}

	pool, err := newPoolWithRetries(ctx, dsn, poolOptions{connectTimeout: 5 * time.Second})
	if err != nil {
		return fmt.Errorf("tenant DSN connection failed: %w", err)
	}
	defer pool.Close()

	if err := verifyTenantRoleFlags(ctx, pool); err != nil {
		return err
	}
	if err := verifyTenantDangerousMemberships(ctx, pool); err != nil {
		return err
	}
	if err := verifyTenantDefaultReadOnly(ctx, pool); err != nil {
		return err
	}
	if err := verifyTenantReadOnlyTransaction(ctx, pool); err != nil {
		return err
	}
	if opts.AllowedSchemas != nil {
		if err := verifyTenantSchemaVisibility(ctx, pool, opts.AllowedSchemas); err != nil {
			return err
		}
	}
	return nil
}

func dsnUsesTLS(dsn string) bool {
	mode := dsnSSLMode(dsn)
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "require", "verify-ca", "verify-full":
		return true
	default:
		return false
	}
}

func dsnSSLMode(dsn string) string {
	dsn = strings.TrimSpace(dsn)
	if u, err := url.Parse(dsn); err == nil && u.Scheme != "" && (u.Scheme == "postgres" || u.Scheme == "postgresql") {
		if m := u.Query().Get("sslmode"); m != "" {
			return m
		}
	}
	// libpq keyword/value DSNs: "host=... sslmode=require"
	for _, field := range strings.Fields(dsn) {
		key, val, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(key), "sslmode") {
			return strings.TrimSpace(val)
		}
	}
	return ""
}

func dsnHost(dsn string) string {
	dsn = strings.TrimSpace(dsn)
	if u, err := url.Parse(dsn); err == nil && u.Scheme != "" && u.Host != "" {
		host := u.Hostname()
		if host != "" {
			return host
		}
	}
	for _, field := range strings.Fields(dsn) {
		key, val, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(key), "host") {
			return strings.TrimSpace(val)
		}
	}
	return ""
}

func verifyTenantDSNHost(dsn string, allowlist []string) error {
	host := dsnHost(dsn)
	if host == "" {
		return fmt.Errorf("tenant DSN host is required when a host allowlist is configured")
	}
	hostLower := strings.ToLower(host)
	for _, entry := range allowlist {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if strings.EqualFold(entry, host) {
			return nil
		}
		if _, cidr, err := net.ParseCIDR(entry); err == nil {
			ip := net.ParseIP(host)
			if ip != nil && cidr.Contains(ip) {
				return nil
			}
			// Resolve hostname and check any A/AAAA against CIDR.
			ips, lookupErr := net.LookupIP(host)
			if lookupErr == nil {
				for _, resolved := range ips {
					if cidr.Contains(resolved) {
						return nil
					}
				}
			}
			continue
		}
		if strings.EqualFold(entry, hostLower) {
			return nil
		}
	}
	return fmt.Errorf("tenant DSN host %q is not allowed by policy", host)
}

func verifyTenantRoleFlags(ctx context.Context, pool *pgxpool.Pool) error {
	var superuser, bypassRLS, createDB, createRole, replication bool
	if err := pool.QueryRow(ctx, `
		SELECT rolsuper, rolbypassrls, rolcreatedb, rolcreaterole, rolreplication
		FROM pg_roles
		WHERE rolname = current_user
	`).Scan(&superuser, &bypassRLS, &createDB, &createRole, &replication); err != nil {
		return fmt.Errorf("tenant DSN role inspection failed: %w", err)
	}
	if superuser || bypassRLS || createDB || createRole || replication {
		return fmt.Errorf("tenant DSN role has forbidden PostgreSQL privileges")
	}
	return nil
}

func verifyTenantDangerousMemberships(ctx context.Context, pool *pgxpool.Pool) error {
	rows, err := pool.Query(ctx, `
		SELECT r.rolname
		FROM pg_auth_members m
		JOIN pg_roles r ON r.oid = m.roleid
		JOIN pg_roles u ON u.oid = m.member
		WHERE u.rolname = current_user
		  AND r.rolname = ANY($1::text[])
	`, dangerousPredefinedRoles)
	if err != nil {
		return fmt.Errorf("tenant DSN role membership inspection failed: %w", err)
	}
	defer rows.Close()
	var found []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return fmt.Errorf("tenant DSN role membership inspection failed: %w", err)
		}
		found = append(found, name)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("tenant DSN role membership inspection failed: %w", err)
	}
	if len(found) > 0 {
		return fmt.Errorf("tenant DSN role has forbidden memberships: %s", strings.Join(found, ", "))
	}
	return nil
}

func verifyTenantDefaultReadOnly(ctx context.Context, pool *pgxpool.Pool) error {
	var defaultRO string
	if err := pool.QueryRow(ctx, `SHOW default_transaction_read_only`).Scan(&defaultRO); err != nil {
		return fmt.Errorf("tenant DSN default_transaction_read_only check failed: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(defaultRO), "on") {
		return fmt.Errorf("tenant DSN role must set default_transaction_read_only=on")
	}
	return nil
}

func verifyTenantReadOnlyTransaction(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("tenant DSN read-only transaction failed: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var readOnly string
	if err := tx.QueryRow(ctx, `SHOW transaction_read_only`).Scan(&readOnly); err != nil {
		return fmt.Errorf("tenant DSN read-only check failed: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(readOnly), "on") {
		return fmt.Errorf("tenant DSN did not enter a read-only transaction")
	}

	if _, err := tx.Exec(ctx, `DELETE FROM pg_catalog.pg_class WHERE false`); err == nil {
		return fmt.Errorf("tenant DSN allowed a write inside a read-only transaction")
	}
	if _, err := tx.Exec(ctx, `CREATE TEMP TABLE pgqn_tenant_probe(id int)`); err == nil {
		return fmt.Errorf("tenant DSN allowed DDL inside a read-only transaction")
	}
	return nil
}

func verifyTenantSchemaVisibility(ctx context.Context, pool *pgxpool.Pool, schemas []string) error {
	for _, schemaName := range schemas {
		schemaName = strings.TrimSpace(schemaName)
		if schemaName == "" {
			continue
		}
		var exists bool
		if err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.schemata WHERE schema_name = $1
			)
		`, schemaName).Scan(&exists); err != nil {
			return fmt.Errorf("tenant DSN schema visibility check failed: %w", err)
		}
		if !exists {
			return fmt.Errorf("tenant DSN does not expose configured schema %q", schemaName)
		}
	}
	return nil
}
