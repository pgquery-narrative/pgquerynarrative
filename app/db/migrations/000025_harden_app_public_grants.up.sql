-- App role only needs app + demo schemas; remove broad public data access.
REVOKE ALL ON SCHEMA public FROM pgquerynarrative_app;
REVOKE SELECT ON ALL TABLES IN SCHEMA public FROM pgquerynarrative_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE ALL ON TABLES FROM pgquerynarrative_app;

-- Extensions (e.g. pgvector) live in public; allow usage only.
GRANT USAGE ON SCHEMA public TO pgquerynarrative_app;
