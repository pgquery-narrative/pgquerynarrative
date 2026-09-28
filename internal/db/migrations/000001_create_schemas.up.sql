-- Create roles first so later migrations can GRANT to them (e.g. 000003).
DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = 'pgquerynarrative_readonly') THEN
        CREATE ROLE pgquerynarrative_readonly LOGIN PASSWORD 'pgquerynarrative_readonly';
    END IF;
    IF NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = 'pgquerynarrative_app') THEN
        CREATE ROLE pgquerynarrative_app LOGIN PASSWORD 'pgquerynarrative_app';
    END IF;
END
$$;

CREATE SCHEMA IF NOT EXISTS app;
CREATE SCHEMA IF NOT EXISTS demo;

GRANT USAGE ON SCHEMA app TO pgquerynarrative_app;
GRANT USAGE ON SCHEMA demo TO pgquerynarrative_readonly;
