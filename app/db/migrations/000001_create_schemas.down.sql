DROP SCHEMA IF EXISTS demo CASCADE;
DROP SCHEMA IF EXISTS app CASCADE;

-- Transfer DB ownership away from app roles before DROP ROLE (migrate cycle uses
-- the app role as database owner after db-init / role grants in some environments).
DO $$
BEGIN
  EXECUTE format('ALTER DATABASE %I OWNER TO CURRENT_USER', current_database());
EXCEPTION
  WHEN insufficient_privilege THEN
    NULL;
  WHEN undefined_object THEN
    NULL;
END $$;

-- Clear default privileges and other dependencies so DROP ROLE can succeed after a full
-- migrate down (e.g. ALTER DEFAULT PRIVILEGES ... GRANT SELECT TO pgquerynarrative_readonly).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'pgquerynarrative_readonly') THEN
    EXECUTE 'DROP OWNED BY pgquerynarrative_readonly CASCADE';
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'pgquerynarrative_app') THEN
    EXECUTE 'DROP OWNED BY pgquerynarrative_app CASCADE';
  END IF;
END $$;

DROP ROLE IF EXISTS pgquerynarrative_readonly;
DROP ROLE IF EXISTS pgquerynarrative_app;
