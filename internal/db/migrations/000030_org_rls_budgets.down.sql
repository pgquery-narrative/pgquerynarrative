DROP TABLE IF EXISTS app.llm_budget_usage;

DROP INDEX IF EXISTS app.idx_schedule_runs_lease;
DROP INDEX IF EXISTS app.idx_webhook_deliveries_retry;
DROP INDEX IF EXISTS app.idx_ask_sessions_org;
DROP INDEX IF EXISTS app.idx_dashboards_org;
DROP INDEX IF EXISTS app.idx_schedules_org;
DROP INDEX IF EXISTS app.idx_reports_org;
DROP INDEX IF EXISTS app.idx_saved_queries_org;

DROP POLICY IF EXISTS llm_audit_events_org ON app.llm_audit_events;
ALTER TABLE app.llm_audit_events DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS webhook_deliveries_org ON app.webhook_deliveries;
ALTER TABLE app.webhook_deliveries DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS schedule_runs_org ON app.schedule_runs;
ALTER TABLE app.schedule_runs DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS ask_sessions_org ON app.ask_sessions;
ALTER TABLE app.ask_sessions DISABLE ROW LEVEL SECURITY;

-- Restore fail-open policies from 000029.
DROP POLICY IF EXISTS dashboards_org ON app.dashboards;
CREATE POLICY dashboards_org ON app.dashboards
    USING (
        organization_id::text = current_setting('app.current_org_id', true)
        OR current_setting('app.current_org_id', true) = ''
    );
ALTER TABLE app.dashboards NO FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS schedules_org ON app.schedules;
CREATE POLICY schedules_org ON app.schedules
    USING (
        organization_id::text = current_setting('app.current_org_id', true)
        OR current_setting('app.current_org_id', true) = ''
    );
ALTER TABLE app.schedules NO FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS reports_org ON app.reports;
CREATE POLICY reports_org ON app.reports
    USING (
        organization_id::text = current_setting('app.current_org_id', true)
        OR current_setting('app.current_org_id', true) = ''
    );
ALTER TABLE app.reports NO FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS saved_queries_org ON app.saved_queries;
CREATE POLICY saved_queries_org ON app.saved_queries
    USING (
        organization_id::text = current_setting('app.current_org_id', true)
        OR current_setting('app.current_org_id', true) = ''
    );
ALTER TABLE app.saved_queries NO FORCE ROW LEVEL SECURITY;

DELETE FROM app.organization_members
WHERE organization_id = '00000000-0000-0000-0000-000000000001'
  AND user_id IN ('api-key', 'system');
