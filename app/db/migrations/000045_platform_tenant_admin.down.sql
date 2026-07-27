-- Revert platform/tenant admin split to legacy admin|analyst|viewer roles.

DROP FUNCTION IF EXISTS app.count_organization_connection_secrets();

DELETE FROM app.connection_permissions
WHERE principal_type = 'role' AND principal_id = 'platform_admin';

UPDATE app.connection_permissions
SET principal_id = 'admin'
WHERE principal_type = 'role' AND principal_id = 'tenant_admin';

ALTER TABLE app.managed_api_keys
    DROP CONSTRAINT IF EXISTS managed_api_keys_role_check;

UPDATE app.managed_api_keys
SET role = 'admin'
WHERE role IN ('platform_admin', 'tenant_admin');

ALTER TABLE app.managed_api_keys
    ADD CONSTRAINT managed_api_keys_role_check
    CHECK (role IN ('admin', 'analyst', 'viewer'));

ALTER TABLE app.oidc_group_org_mappings
    DROP CONSTRAINT IF EXISTS oidc_group_org_mappings_role_check;

UPDATE app.oidc_group_org_mappings
SET role = 'admin'
WHERE role IN ('platform_admin', 'tenant_admin');

ALTER TABLE app.oidc_group_org_mappings
    ADD CONSTRAINT oidc_group_org_mappings_role_check
    CHECK (role IN ('admin', 'analyst', 'viewer'));

ALTER TABLE app.organization_members
    DROP CONSTRAINT IF EXISTS organization_members_role_check;

UPDATE app.organization_members
SET role = 'admin'
WHERE role IN ('platform_admin', 'tenant_admin');

ALTER TABLE app.organization_members
    ADD CONSTRAINT organization_members_role_check
    CHECK (role IN ('admin', 'analyst', 'viewer'));
