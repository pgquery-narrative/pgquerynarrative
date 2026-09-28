DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'pgquerynarrative_readonly') THEN
    REVOKE pg_read_all_stats FROM pgquerynarrative_readonly;
  END IF;
END $$;
