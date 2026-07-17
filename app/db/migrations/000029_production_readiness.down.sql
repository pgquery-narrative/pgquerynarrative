DROP POLICY IF EXISTS dashboards_org ON app.dashboards;
DROP POLICY IF EXISTS schedules_org ON app.schedules;
DROP POLICY IF EXISTS reports_org ON app.reports;
DROP POLICY IF EXISTS saved_queries_org ON app.saved_queries;

ALTER TABLE app.dashboards DISABLE ROW LEVEL SECURITY;
ALTER TABLE app.schedules DISABLE ROW LEVEL SECURITY;
ALTER TABLE app.reports DISABLE ROW LEVEL SECURITY;
ALTER TABLE app.saved_queries DISABLE ROW LEVEL SECURITY;

DROP TABLE IF EXISTS app.llm_audit_events;
DROP TABLE IF EXISTS app.webhook_deliveries;
DROP TABLE IF EXISTS app.schedule_runs;

ALTER TABLE app.schedules
    DROP COLUMN IF EXISTS locked_by,
    DROP COLUMN IF EXISTS locked_until;

ALTER TABLE app.ask_sessions DROP COLUMN IF EXISTS organization_id;
ALTER TABLE app.dashboards DROP COLUMN IF EXISTS organization_id;
ALTER TABLE app.schedules DROP COLUMN IF EXISTS organization_id;
ALTER TABLE app.reports DROP COLUMN IF EXISTS organization_id;
ALTER TABLE app.saved_queries DROP COLUMN IF EXISTS organization_id;

DROP TABLE IF EXISTS app.organization_members;
DROP TABLE IF EXISTS app.organizations;
