-- Provenance for investigations opened from a PostgREST read request.
-- binds: captured bind values for a parameterized source SQL ($1, $2, ...).
-- postgrest_request: sanitized request provenance (method, path, role, timings).
-- Headers, cookies and JWT claims are never written to either column.

ALTER TABLE app.investigations
    ADD COLUMN IF NOT EXISTS binds JSONB,
    ADD COLUMN IF NOT EXISTS postgrest_request JSONB;
