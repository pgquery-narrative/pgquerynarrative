-- Allow read-only user to query tables in public schema (user data).
GRANT USAGE ON SCHEMA public TO pgquerynarrative_readonly;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO pgquerynarrative_readonly;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT SELECT ON TABLES TO pgquerynarrative_readonly;
