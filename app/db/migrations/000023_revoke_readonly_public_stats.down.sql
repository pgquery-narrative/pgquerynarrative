GRANT USAGE ON SCHEMA public TO pgquerynarrative_readonly;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO pgquerynarrative_readonly;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT SELECT ON TABLES TO pgquerynarrative_readonly;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'pgquerynarrative_readonly') THEN
    GRANT pg_read_all_stats TO pgquerynarrative_readonly;
  END IF;
END $$;
