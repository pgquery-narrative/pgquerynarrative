-- Production hardening: readonly role must not access public schema or global pg_stat_statements.

REVOKE ALL ON SCHEMA public FROM pgquerynarrative_readonly;
REVOKE SELECT ON ALL TABLES IN SCHEMA public FROM pgquerynarrative_readonly;
ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE SELECT ON TABLES FROM pgquerynarrative_readonly;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'pgquerynarrative_readonly') THEN
    REVOKE pg_read_all_stats FROM pgquerynarrative_readonly;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'pgquerynarrative_app') THEN
    GRANT pg_read_all_stats TO pgquerynarrative_app;
  END IF;
END $$;
