DROP POLICY IF EXISTS explain_snapshots_org ON app.explain_snapshots;
CREATE POLICY explain_snapshots_org ON app.explain_snapshots
    USING (organization_id::text = NULLIF(current_setting('app.current_org_id', true), ''));

REVOKE DELETE ON app.explain_snapshots FROM pgquerynarrative_app;
