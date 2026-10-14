ALTER TABLE app.investigations
    DROP COLUMN IF EXISTS postgrest_request,
    DROP COLUMN IF EXISTS binds;
