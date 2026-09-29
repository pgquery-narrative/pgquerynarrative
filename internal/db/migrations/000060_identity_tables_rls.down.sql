DROP POLICY IF EXISTS audit_log_buffer_org ON app.audit_log_buffer;
ALTER TABLE app.audit_log_buffer NO FORCE ROW LEVEL SECURITY;
ALTER TABLE app.audit_log_buffer DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS oidc_group_org_mappings_org ON app.oidc_group_org_mappings;
DROP POLICY IF EXISTS oidc_group_org_mappings_select ON app.oidc_group_org_mappings;
DROP POLICY IF EXISTS oidc_group_org_mappings_insert ON app.oidc_group_org_mappings;
DROP POLICY IF EXISTS oidc_group_org_mappings_update ON app.oidc_group_org_mappings;
DROP POLICY IF EXISTS oidc_group_org_mappings_delete ON app.oidc_group_org_mappings;
ALTER TABLE app.oidc_group_org_mappings NO FORCE ROW LEVEL SECURITY;
ALTER TABLE app.oidc_group_org_mappings DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS organization_members_org ON app.organization_members;
DROP POLICY IF EXISTS organization_members_select ON app.organization_members;
DROP POLICY IF EXISTS organization_members_insert ON app.organization_members;
DROP POLICY IF EXISTS organization_members_update ON app.organization_members;
DROP POLICY IF EXISTS organization_members_delete ON app.organization_members;
ALTER TABLE app.organization_members NO FORCE ROW LEVEL SECURITY;
ALTER TABLE app.organization_members DISABLE ROW LEVEL SECURITY;
