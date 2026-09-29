DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'pgquerynarrative_app') THEN
    REVOKE EXECUTE ON FUNCTION app.revoke_browser_session(uuid) FROM pgquerynarrative_app;
  END IF;
END
$$;
