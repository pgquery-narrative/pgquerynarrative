-- Isolation MVP: seed default-org connection allowlist, and Phase 2 per-org DSN secrets.

-- Single-team deploys keep working when SECURITY_CONNECTION_ALLOWLIST_REQUIRED=true.
INSERT INTO app.organization_connections (organization_id, connection_id, enabled)
VALUES ('00000000-0000-0000-0000-000000000001', 'default', true)
ON CONFLICT (organization_id, connection_id) DO UPDATE SET enabled = true;

-- Seeding organization_connections flips the default org out of bootstrap mode
-- (empty allowlist = allow all). Non-admin roles then need explicit grants.
-- Admin already has a wildcard grant from migration 000034; add analyst + viewer.
INSERT INTO app.connection_permissions (
    organization_id, connection_id, principal_type, principal_id,
    can_query, can_explain, can_analyze, can_schema, can_report, can_schedule, can_stats, can_ask
)
SELECT
    '00000000-0000-0000-0000-000000000001'::uuid, NULL, 'role', 'analyst',
    true, true, true, true, true, true, true, true
WHERE NOT EXISTS (
    SELECT 1 FROM app.connection_permissions
    WHERE organization_id = '00000000-0000-0000-0000-000000000001'::uuid
      AND connection_id IS NULL
      AND principal_type = 'role'
      AND principal_id = 'analyst'
);

INSERT INTO app.connection_permissions (
    organization_id, connection_id, principal_type, principal_id,
    can_query, can_explain, can_analyze, can_schema, can_report, can_schedule, can_stats, can_ask
)
SELECT
    '00000000-0000-0000-0000-000000000001'::uuid, NULL, 'role', 'viewer',
    true, true, false, true, true, false, true, false
WHERE NOT EXISTS (
    SELECT 1 FROM app.connection_permissions
    WHERE organization_id = '00000000-0000-0000-0000-000000000001'::uuid
      AND connection_id IS NULL
      AND principal_type = 'role'
      AND principal_id = 'viewer'
);

-- Per-org encrypted connection secrets (Phase 2). Catalog connection_id may be shared
-- or org-owned; sealed_dsn holds the AES-GCM Seal envelope of a postgres URL.
CREATE TABLE IF NOT EXISTS app.organization_connection_secrets (
    organization_id UUID NOT NULL REFERENCES app.organizations(id) ON DELETE CASCADE,
    connection_id TEXT NOT NULL,
    sealed_dsn TEXT NOT NULL,
    allowed_schemas JSONB NOT NULL DEFAULT '[]'::jsonb,
    enabled BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (organization_id, connection_id)
);

CREATE INDEX IF NOT EXISTS idx_organization_connection_secrets_org
    ON app.organization_connection_secrets(organization_id);

ALTER TABLE app.organization_connection_secrets ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.organization_connection_secrets FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS organization_connection_secrets_org ON app.organization_connection_secrets;
CREATE POLICY organization_connection_secrets_org ON app.organization_connection_secrets
    USING (organization_id::text = NULLIF(current_setting('app.current_org_id', true), ''));

GRANT SELECT, INSERT, UPDATE, DELETE ON app.organization_connection_secrets TO pgquerynarrative_app;
