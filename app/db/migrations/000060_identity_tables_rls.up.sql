-- Row-level security for the three tables that carry an organization_id but had none:
-- organization_members (who holds which role where), oidc_group_org_mappings (which IdP group maps to
-- which organization and role) and audit_log_buffer (audit entries waiting to be written).
--
-- A missing WHERE clause in a tenant-facing query used to be the only thing standing between one
-- organization and another's roles. The policies below match the rest of the schema: a row is visible
-- and writable inside its own organization (app.current_org_id, set per transaction).
--
-- Two narrow read-only exceptions exist because login resolves an identity before an organization is
-- chosen:
--   organization_members     : the caller's own rows in every organization, when the login lookup sets
--                             app.membership_user_id
--   oidc_group_org_mappings : the mappings for the IdP groups in the token, when the login lookup sets
--                             app.oidc_groups (unit-separator delimited)
-- Neither exception lets anything be written outside the current organization (WITH CHECK).
-- audit_log_buffer is only touched by the audit writer and its replay worker; the worker claims work
-- across organizations with app.scheduler_bypass, as the schedule runner does.
--
-- Superusers (and BYPASSRLS roles) are unaffected, so operators can still provision mappings with psql.

ALTER TABLE app.organization_members ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.organization_members FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS organization_members_org ON app.organization_members;
CREATE POLICY organization_members_org ON app.organization_members
    USING (
        organization_id::text = NULLIF(current_setting('app.current_org_id', true), '')
        OR user_id = NULLIF(current_setting('app.membership_user_id', true), '')
    )
    WITH CHECK (organization_id::text = NULLIF(current_setting('app.current_org_id', true), ''));

ALTER TABLE app.oidc_group_org_mappings ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.oidc_group_org_mappings FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS oidc_group_org_mappings_org ON app.oidc_group_org_mappings;
CREATE POLICY oidc_group_org_mappings_org ON app.oidc_group_org_mappings
    USING (
        organization_id::text = NULLIF(current_setting('app.current_org_id', true), '')
        OR group_claim = ANY (string_to_array(NULLIF(current_setting('app.oidc_groups', true), ''), E'\x1f'))
    )
    WITH CHECK (organization_id::text = NULLIF(current_setting('app.current_org_id', true), ''));

ALTER TABLE app.audit_log_buffer ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.audit_log_buffer FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS audit_log_buffer_org ON app.audit_log_buffer;
CREATE POLICY audit_log_buffer_org ON app.audit_log_buffer
    USING (
        organization_id::text = NULLIF(current_setting('app.current_org_id', true), '')
        OR current_setting('app.scheduler_bypass', true) = 'true'
    )
    WITH CHECK (
        organization_id::text = NULLIF(current_setting('app.current_org_id', true), '')
        OR current_setting('app.scheduler_bypass', true) = 'true'
    );
