package testhelpers

import (
	"context"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
)

// EnsurePostgresRoles creates application roles used in production-like integration tests.
func EnsurePostgresRoles(ctx context.Context, adminPool *pgxpool.Pool) error {
	_, err := adminPool.Exec(ctx, `
DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = 'pgquerynarrative_app') THEN
        CREATE ROLE pgquerynarrative_app LOGIN PASSWORD 'pgquerynarrative_app';
    END IF;
    IF NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = 'pgquerynarrative_readonly') THEN
        CREATE ROLE pgquerynarrative_readonly LOGIN PASSWORD 'pgquerynarrative_readonly';
    END IF;
END $$;
ALTER ROLE pgquerynarrative_app WITH LOGIN PASSWORD 'pgquerynarrative_app' NOSUPERUSER NOBYPASSRLS;
ALTER ROLE pgquerynarrative_readonly WITH LOGIN PASSWORD 'pgquerynarrative_readonly' NOSUPERUSER NOBYPASSRLS;
CREATE SCHEMA IF NOT EXISTS app;
CREATE SCHEMA IF NOT EXISTS demo;
GRANT USAGE ON SCHEMA app TO pgquerynarrative_app;
GRANT ALL ON ALL TABLES IN SCHEMA app TO pgquerynarrative_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA app GRANT ALL ON TABLES TO pgquerynarrative_app;
GRANT USAGE ON SCHEMA public TO pgquerynarrative_app;
GRANT SELECT ON TABLE public.schema_migrations TO pgquerynarrative_app;
`)
	return err
}

// AppPoolFromAdmin opens a pgquerynarrative_app pool for RLS-enforced tests.
func AppPoolFromAdmin(ctx context.Context, adminPool *pgxpool.Pool, connStr string) (*pgxpool.Pool, error) {
	if err := EnsurePostgresRoles(ctx, adminPool); err != nil {
		return nil, err
	}
	if os.Getenv("DOCKER_API_VERSION") == "" {
		_ = os.Setenv("DOCKER_API_VERSION", "1.44")
	}
	cfg, err := pgxpool.ParseConfig(connStr)
	if err != nil {
		return nil, err
	}
	cfg.ConnConfig.User = "pgquerynarrative_app"
	cfg.ConnConfig.Password = "pgquerynarrative_app"
	return pgxpool.NewWithConfig(ctx, cfg)
}
