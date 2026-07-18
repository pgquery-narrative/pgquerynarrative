-- Connection-level authorization (C5): app metadata is org-scoped, but without
-- this table a user in any org could select any configured DB connection ID on
-- queries/reports/schema/ask/schedules. These tables let an org opt into an
-- explicit connection allowlist and grant per-principal (user/role/team) actions.
--
-- Authorization semantics (enforced in Go by app/auth/connection_authz.go, not by
-- RLS alone -- RLS here only provides tenant isolation between rows of these two
-- tables, mirroring the pattern used for every other app.* table):
--   1. Bootstrap: if an organization has ZERO rows in organization_connections,
--      every configured connection is allowed for that org (keeps existing
--      single-team deployments working without any admin setup).
--   2. Once an organization has at least one organization_connections row,
--      enforcement begins for that org: a connection must have an enabled=true
--      row for (organization_id, connection_id), otherwise access is denied
--      (this also covers connections that belong only to a different org).
--   3. Within an org connection that is assigned, 'admin' role principals are
--      always allowed (no explicit grant needed). Non-admin principals (analyst,
--      viewer, and any future roles) require a matching, action-flagged row in
--      connection_permissions for principal_type/principal_id ('user' with the
--      user id, or 'role' with the role name). connection_id = NULL in
--      connection_permissions is a wildcard meaning "all connections for this org".

CREATE TABLE IF NOT EXISTS app.organization_connections (
    organization_id UUID NOT NULL REFERENCES app.organizations(id) ON DELETE CASCADE,
    connection_id TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (organization_id, connection_id)
);

CREATE INDEX IF NOT EXISTS idx_organization_connections_org ON app.organization_connections(organization_id);

ALTER TABLE app.organization_connections ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.organization_connections FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS organization_connections_org ON app.organization_connections;
CREATE POLICY organization_connections_org ON app.organization_connections
    USING (organization_id::text = NULLIF(current_setting('app.current_org_id', true), ''));

GRANT SELECT, INSERT, UPDATE, DELETE ON app.organization_connections TO pgquerynarrative_app;

CREATE TABLE IF NOT EXISTS app.connection_permissions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES app.organizations(id) ON DELETE CASCADE,
    -- NULL connection_id is a wildcard: this grant applies to every connection in the org.
    connection_id TEXT,
    principal_type TEXT NOT NULL CHECK (principal_type IN ('user', 'role', 'team')),
    principal_id TEXT NOT NULL,
    query BOOLEAN NOT NULL DEFAULT false,
    explain BOOLEAN NOT NULL DEFAULT false,
    analyze BOOLEAN NOT NULL DEFAULT false,
    schema BOOLEAN NOT NULL DEFAULT false,
    report BOOLEAN NOT NULL DEFAULT false,
    schedule BOOLEAN NOT NULL DEFAULT false,
    stats BOOLEAN NOT NULL DEFAULT false,
    ask BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_connection_permissions_lookup
    ON app.connection_permissions(organization_id, connection_id, principal_type, principal_id);

-- Prevents duplicate grants for the same (org, connection-or-wildcard, principal) and
-- gives the bootstrap seed below an idempotent upsert target.
CREATE UNIQUE INDEX IF NOT EXISTS uq_connection_permissions_principal
    ON app.connection_permissions (organization_id, COALESCE(connection_id, ''), principal_type, principal_id);

ALTER TABLE app.connection_permissions ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.connection_permissions FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS connection_permissions_org ON app.connection_permissions;
CREATE POLICY connection_permissions_org ON app.connection_permissions
    USING (organization_id::text = NULLIF(current_setting('app.current_org_id', true), ''));

GRANT SELECT, INSERT, UPDATE, DELETE ON app.connection_permissions TO pgquerynarrative_app;

-- Seed: give the admin role a wildcard, all-actions grant in the default org. Admins
-- are already always-allowed within an org per the rules above once a connection is
-- assigned to that org (step 3); this row additionally covers the moment an operator
-- adds organization_connections rows for the default org and flips it from bootstrap
-- into enforcement, so existing single-team deploys keep working without a manual
-- permissions setup step for their admin user(s).
INSERT INTO app.connection_permissions (
    organization_id, connection_id, principal_type, principal_id,
    query, explain, analyze, schema, report, schedule, stats, ask
) VALUES (
    '00000000-0000-0000-0000-000000000001', NULL, 'role', 'admin',
    true, true, true, true, true, true, true, true
)
ON CONFLICT (organization_id, COALESCE(connection_id, ''), principal_type, principal_id) DO NOTHING;
