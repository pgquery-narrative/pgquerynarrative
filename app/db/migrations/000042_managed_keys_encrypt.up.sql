-- Managed API keys (hashed at rest), EXPLAIN snapshot encryption class,
-- and connection-authz / membership audit support tables already exist.

CREATE TABLE IF NOT EXISTS app.managed_api_keys (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES app.organizations(id) ON DELETE CASCADE,
    key_hash TEXT NOT NULL,
    prefix TEXT NOT NULL,
    role TEXT NOT NULL DEFAULT 'analyst',
    scopes TEXT[] NOT NULL DEFAULT '{}',
    expires_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    created_by TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT managed_api_keys_role_check CHECK (role IN ('admin', 'analyst', 'viewer'))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_managed_api_keys_key_hash
    ON app.managed_api_keys(key_hash);
CREATE INDEX IF NOT EXISTS idx_managed_api_keys_org
    ON app.managed_api_keys(organization_id);

ALTER TABLE app.managed_api_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.managed_api_keys FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS managed_api_keys_org ON app.managed_api_keys;
CREATE POLICY managed_api_keys_org ON app.managed_api_keys
    USING (organization_id = NULLIF(current_setting('app.current_org_id', true), '')::uuid);

REVOKE ALL ON TABLE app.managed_api_keys FROM PUBLIC;
GRANT SELECT, INSERT, UPDATE ON TABLE app.managed_api_keys TO pgquerynarrative_app;

-- Hash-based resolve for authentication (no org context yet). SECURITY DEFINER
-- so FORCE RLS does not block lookup by unique key_hash.
CREATE OR REPLACE FUNCTION app.resolve_managed_api_key(p_key_hash text)
RETURNS TABLE (
    id uuid,
    organization_id uuid,
    role text,
    prefix text,
    scopes text[],
    expires_at timestamptz,
    revoked_at timestamptz
)
LANGUAGE sql
SECURITY DEFINER
SET search_path = app, public
AS $$
    SELECT k.id, k.organization_id, k.role, k.prefix, k.scopes, k.expires_at, k.revoked_at
    FROM app.managed_api_keys k
    WHERE k.key_hash = p_key_hash
    LIMIT 1;
$$;
REVOKE ALL ON FUNCTION app.resolve_managed_api_key(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.resolve_managed_api_key(text) TO pgquerynarrative_app;

-- Allow encrypted storage class for EXPLAIN snapshots.
ALTER TABLE app.explain_snapshots
    DROP CONSTRAINT IF EXISTS explain_snapshots_sql_storage_class_check;
ALTER TABLE app.explain_snapshots
    ADD CONSTRAINT explain_snapshots_sql_storage_class_check
    CHECK (sql_storage_class IN ('raw', 'redacted', 'fingerprint', 'encrypted'));
