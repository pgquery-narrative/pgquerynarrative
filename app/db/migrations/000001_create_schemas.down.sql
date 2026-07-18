DROP SCHEMA IF EXISTS demo CASCADE;
DROP SCHEMA IF EXISTS app CASCADE;

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
