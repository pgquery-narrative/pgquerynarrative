-- Ready/Health checks read schema_migrations via the app role.
-- 000025 revoked public table access; restore SELECT on migrate's version table only.
GRANT SELECT ON TABLE public.schema_migrations TO pgquerynarrative_app;
