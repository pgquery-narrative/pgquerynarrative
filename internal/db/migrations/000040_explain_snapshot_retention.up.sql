-- Support retention cleanup of stored EXPLAIN snapshots: allow the app role to delete
-- old rows, and let the cross-org background cleanup job (see
-- service.StartExplainSnapshotCleanupLoop) bypass the per-organization RLS policy the
-- same way other scheduled/background jobs do (app.scheduler_bypass session flag).

GRANT DELETE ON app.explain_snapshots TO pgquerynarrative_app;

DROP POLICY IF EXISTS explain_snapshots_org ON app.explain_snapshots;
CREATE POLICY explain_snapshots_org ON app.explain_snapshots
    USING (
        organization_id::text = NULLIF(current_setting('app.current_org_id', true), '')
        OR current_setting('app.scheduler_bypass', true) = 'true'
    );
