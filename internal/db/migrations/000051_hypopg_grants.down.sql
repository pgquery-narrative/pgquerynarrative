DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'hypopg') THEN
    BEGIN
      IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'pgquerynarrative_readonly') THEN
        REVOKE EXECUTE ON FUNCTION hypopg_create_index(text) FROM pgquerynarrative_readonly;
        REVOKE EXECUTE ON FUNCTION hypopg_reset() FROM pgquerynarrative_readonly;
      END IF;
      IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'pgquerynarrative_app') THEN
        REVOKE EXECUTE ON FUNCTION hypopg_create_index(text) FROM pgquerynarrative_app;
        REVOKE EXECUTE ON FUNCTION hypopg_reset() FROM pgquerynarrative_app;
      END IF;
    EXCEPTION WHEN OTHERS THEN
      RAISE NOTICE 'skip hypopg REVOKE: %', SQLERRM;
    END;
  END IF;
END $$;
