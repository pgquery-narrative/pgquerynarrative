-- Revoke public schema access from read-only user.
REVOKE SELECT ON ALL TABLES IN SCHEMA public FROM pgquerynarrative_readonly;
REVOKE USAGE ON SCHEMA public FROM pgquerynarrative_readonly;
