DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'pgquerynarrative_app') THEN
    GRANT UPDATE, DELETE, TRUNCATE ON app.audit_logs TO pgquerynarrative_app;
  END IF;
END
$$;
