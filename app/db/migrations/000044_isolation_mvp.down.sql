DROP POLICY IF EXISTS organization_connection_secrets_org ON app.organization_connection_secrets;
DROP TABLE IF EXISTS app.organization_connection_secrets;

DELETE FROM app.connection_permissions
WHERE organization_id = '00000000-0000-0000-0000-000000000001'
  AND connection_id = 'default'
  AND principal_type = 'role'
  AND principal_id IN ('analyst', 'viewer');

DELETE FROM app.organization_connections
WHERE organization_id = '00000000-0000-0000-0000-000000000001'
  AND connection_id = 'default';
