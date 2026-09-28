DROP TABLE IF EXISTS opendata.yellow_trips CASCADE;
DROP SCHEMA IF EXISTS opendata CASCADE;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'pgquerynarrative_readonly') THEN
    ALTER ROLE pgquerynarrative_readonly SET search_path = demo;
  END IF;
END $$;
