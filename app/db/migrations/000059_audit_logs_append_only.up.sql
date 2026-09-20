-- Migration 000033 meant the app role to have SELECT and INSERT on app.audit_logs, but the default
-- privileges set by infra/postgres-init (GRANT ALL ON TABLES) left it UPDATE, DELETE and TRUNCATE
-- as well, so the role could edit and erase its own audit trail. The application never does either.
--
-- This stops accidental deletion and SQL injected as the app role. A role that OWNS the table can
-- grant the privileges back to itself, so for tamper resistance against a compromised app role run
-- the migrations as a different owner.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'pgquerynarrative_app') THEN
    REVOKE UPDATE, DELETE, TRUNCATE ON app.audit_logs FROM pgquerynarrative_app;
  END IF;
END
$$;
