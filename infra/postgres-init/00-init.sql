DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = 'pgquerynarrative_app') THEN
        CREATE ROLE pgquerynarrative_app LOGIN PASSWORD 'pgquerynarrative_app';
    END IF;
    
    IF NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = 'pgquerynarrative_readonly') THEN
        CREATE ROLE pgquerynarrative_readonly LOGIN PASSWORD 'pgquerynarrative_readonly';
    END IF;
END
$$;

ALTER DATABASE pgquerynarrative OWNER TO pgquerynarrative_app;

CREATE SCHEMA IF NOT EXISTS app;
CREATE SCHEMA IF NOT EXISTS demo;

ALTER SCHEMA app OWNER TO pgquerynarrative_app;
ALTER SCHEMA demo OWNER TO pgquerynarrative_app;

GRANT USAGE ON SCHEMA app TO pgquerynarrative_app;
GRANT ALL ON ALL TABLES IN SCHEMA app TO pgquerynarrative_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA app
    GRANT ALL ON TABLES TO pgquerynarrative_app;

GRANT USAGE ON SCHEMA public TO pgquerynarrative_app;

-- Readonly: demo schema only (no public / app access).
GRANT USAGE ON SCHEMA demo TO pgquerynarrative_readonly;
GRANT SELECT ON ALL TABLES IN SCHEMA demo TO pgquerynarrative_readonly;
ALTER DEFAULT PRIVILEGES IN SCHEMA demo
    GRANT SELECT ON TABLES TO pgquerynarrative_readonly;

-- Fail closed: query role must not touch app metadata.
REVOKE ALL ON SCHEMA app FROM pgquerynarrative_readonly;
