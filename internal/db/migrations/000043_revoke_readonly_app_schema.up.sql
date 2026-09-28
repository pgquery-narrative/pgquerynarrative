-- Harden readonly role: never read app metadata schema (saved queries, reports, keys, etc.).
REVOKE ALL ON SCHEMA app FROM pgquerynarrative_readonly;
REVOKE ALL ON ALL TABLES IN SCHEMA app FROM pgquerynarrative_readonly;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA app FROM pgquerynarrative_readonly;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA app FROM pgquerynarrative_readonly;
ALTER DEFAULT PRIVILEGES IN SCHEMA app REVOKE ALL ON TABLES FROM pgquerynarrative_readonly;
ALTER DEFAULT PRIVILEGES IN SCHEMA app REVOKE ALL ON SEQUENCES FROM pgquerynarrative_readonly;
ALTER DEFAULT PRIVILEGES IN SCHEMA app REVOKE ALL ON FUNCTIONS FROM pgquerynarrative_readonly;

-- Keep public schema closed for the query role (reporting schemas are granted explicitly).
REVOKE ALL ON SCHEMA public FROM pgquerynarrative_readonly;
REVOKE ALL ON ALL TABLES IN SCHEMA public FROM pgquerynarrative_readonly;
