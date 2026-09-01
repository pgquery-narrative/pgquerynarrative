-- Grant hypopg execute rights to the analytical read-only role when the
-- extension is installed. Without this, ProjectIndexCost silently falls back
-- to a labeled heuristic even though CREATE EXTENSION succeeded.

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'hypopg') THEN
    RAISE NOTICE 'hypopg not installed — skip grants (index projection stays heuristic)';
    RETURN;
  END IF;

  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'pgquerynarrative_readonly') THEN
    BEGIN
      -- hypopg functions live in public on stock installs.
      GRANT EXECUTE ON FUNCTION hypopg_create_index(text) TO pgquerynarrative_readonly;
      GRANT EXECUTE ON FUNCTION hypopg_reset() TO pgquerynarrative_readonly;
      -- Optional helpers used by some hypopg versions / tooling.
      BEGIN
        GRANT EXECUTE ON FUNCTION hypopg_list_indexes() TO pgquerynarrative_readonly;
      EXCEPTION WHEN undefined_function THEN
        NULL;
      END;
      BEGIN
        GRANT EXECUTE ON FUNCTION hypopg_drop_index(oid) TO pgquerynarrative_readonly;
      EXCEPTION WHEN undefined_function THEN
        NULL;
      END;
      RAISE NOTICE 'granted hypopg execute to pgquerynarrative_readonly';
    EXCEPTION
      WHEN insufficient_privilege THEN
        RAISE NOTICE 'skip hypopg GRANT to readonly (need superuser migrate)';
      WHEN undefined_function THEN
        RAISE NOTICE 'hypopg functions missing — skip GRANT';
      WHEN OTHERS THEN
        RAISE NOTICE 'skip hypopg GRANT to readonly: %', SQLERRM;
    END;
  END IF;

  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'pgquerynarrative_app') THEN
    BEGIN
      GRANT EXECUTE ON FUNCTION hypopg_create_index(text) TO pgquerynarrative_app;
      GRANT EXECUTE ON FUNCTION hypopg_reset() TO pgquerynarrative_app;
    EXCEPTION WHEN OTHERS THEN
      RAISE NOTICE 'skip hypopg GRANT to app: %', SQLERRM;
    END;
  END IF;
END $$;
