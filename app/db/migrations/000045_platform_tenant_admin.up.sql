-- Split organisation admin into platform_admin vs tenant_admin.
-- Legacy role "admin" on memberships becomes tenant_admin (org-scoped).
-- Platform-wide administration requires an explicit platform_admin role.

ALTER TABLE app.organization_members
    DROP CONSTRAINT IF EXISTS organization_members_role_check;

UPDATE app.organization_members
SET role = 'tenant_admin'
WHERE lower(role) IN ('admin', 'administrator');

ALTER TABLE app.organization_members
    ADD CONSTRAINT organization_members_role_check
    CHECK (role IN ('platform_admin', 'tenant_admin', 'analyst', 'viewer'));

ALTER TABLE app.oidc_group_org_mappings
    DROP CONSTRAINT IF EXISTS oidc_group_org_mappings_role_check;

UPDATE app.oidc_group_org_mappings
SET role = 'tenant_admin'
WHERE lower(role) IN ('admin', 'administrator');

ALTER TABLE app.oidc_group_org_mappings
    ADD CONSTRAINT oidc_group_org_mappings_role_check
    CHECK (role IN ('platform_admin', 'tenant_admin', 'analyst', 'viewer'));

ALTER TABLE app.managed_api_keys
    DROP CONSTRAINT IF EXISTS managed_api_keys_role_check;

UPDATE app.managed_api_keys
SET role = 'tenant_admin'
WHERE lower(role) IN ('admin', 'administrator');

ALTER TABLE app.managed_api_keys
    ADD CONSTRAINT managed_api_keys_role_check
    CHECK (role IN ('platform_admin', 'tenant_admin', 'analyst', 'viewer'));

-- Connection role principals: keep grants usable for both admin classes.
UPDATE app.connection_permissions
SET principal_id = 'tenant_admin'
WHERE principal_type = 'role' AND lower(principal_id) IN ('admin', 'administrator');

INSERT INTO app.connection_permissions (
    organization_id, connection_id, principal_type, principal_id,
    can_query, can_explain, can_analyze, can_schema, can_report, can_schedule, can_stats, can_ask
)
SELECT
    organization_id, connection_id, 'role', 'platform_admin',
    can_query, can_explain, can_analyze, can_schema, can_report, can_schedule, can_stats, can_ask
FROM app.connection_permissions cp
WHERE principal_type = 'role' AND principal_id = 'tenant_admin'
  AND NOT EXISTS (
    SELECT 1 FROM app.connection_permissions x
    WHERE x.organization_id = cp.organization_id
      AND COALESCE(x.connection_id, '') = COALESCE(cp.connection_id, '')
      AND x.principal_type = 'role'
      AND x.principal_id = 'platform_admin'
  );

-- Startup gate: app role must detect tenant secrets even under FORCE RLS.
CREATE OR REPLACE FUNCTION app.count_organization_connection_secrets()
RETURNS bigint
LANGUAGE sql
SECURITY DEFINER
SET search_path = app, pg_temp
AS $$
  SELECT COUNT(*)::bigint FROM app.organization_connection_secrets;
$$;

REVOKE ALL ON FUNCTION app.count_organization_connection_secrets() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.count_organization_connection_secrets() TO pgquerynarrative_app;
