-- Production hardening: fail-closed RLS, membership seed, LLM budgets, retry indexes.

-- Seed default-org membership for bootstrap API-key identity.
INSERT INTO app.organization_members (organization_id, user_id, role)
VALUES ('00000000-0000-0000-0000-000000000001', 'api-key', 'admin')
ON CONFLICT (organization_id, user_id) DO NOTHING;

INSERT INTO app.organization_members (organization_id, user_id, role)
VALUES ('00000000-0000-0000-0000-000000000001', 'system', 'admin')
ON CONFLICT (organization_id, user_id) DO NOTHING;

-- Fail-closed RLS: empty app.current_org_id must not expose rows.
-- Background workers may set app.scheduler_bypass=true inside a transaction.
DROP POLICY IF EXISTS saved_queries_org ON app.saved_queries;
CREATE POLICY saved_queries_org ON app.saved_queries
    USING (organization_id::text = NULLIF(current_setting('app.current_org_id', true), ''));

DROP POLICY IF EXISTS reports_org ON app.reports;
CREATE POLICY reports_org ON app.reports
    USING (organization_id::text = NULLIF(current_setting('app.current_org_id', true), ''));

DROP POLICY IF EXISTS schedules_org ON app.schedules;
CREATE POLICY schedules_org ON app.schedules
    USING (
        organization_id::text = NULLIF(current_setting('app.current_org_id', true), '')
        OR current_setting('app.scheduler_bypass', true) = 'true'
    );

DROP POLICY IF EXISTS dashboards_org ON app.dashboards;
CREATE POLICY dashboards_org ON app.dashboards
    USING (organization_id::text = NULLIF(current_setting('app.current_org_id', true), ''));

ALTER TABLE app.saved_queries FORCE ROW LEVEL SECURITY;
ALTER TABLE app.reports FORCE ROW LEVEL SECURITY;
ALTER TABLE app.schedules FORCE ROW LEVEL SECURITY;
ALTER TABLE app.dashboards FORCE ROW LEVEL SECURITY;

-- Extend RLS to ask sessions and delivery audit tables.
ALTER TABLE app.ask_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.ask_sessions FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS ask_sessions_org ON app.ask_sessions;
CREATE POLICY ask_sessions_org ON app.ask_sessions
    USING (organization_id::text = NULLIF(current_setting('app.current_org_id', true), ''));

ALTER TABLE app.schedule_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.schedule_runs FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS schedule_runs_org ON app.schedule_runs;
CREATE POLICY schedule_runs_org ON app.schedule_runs
    USING (
        organization_id::text = NULLIF(current_setting('app.current_org_id', true), '')
        OR current_setting('app.scheduler_bypass', true) = 'true'
    );

ALTER TABLE app.webhook_deliveries ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.webhook_deliveries FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS webhook_deliveries_org ON app.webhook_deliveries;
CREATE POLICY webhook_deliveries_org ON app.webhook_deliveries
    USING (
        organization_id::text = NULLIF(current_setting('app.current_org_id', true), '')
        OR current_setting('app.scheduler_bypass', true) = 'true'
    );

ALTER TABLE app.llm_audit_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.llm_audit_events FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS llm_audit_events_org ON app.llm_audit_events;
CREATE POLICY llm_audit_events_org ON app.llm_audit_events
    USING (
        organization_id::text = NULLIF(current_setting('app.current_org_id', true), '')
        OR current_setting('app.scheduler_bypass', true) = 'true'
    );

-- Org indexes for IDOR-scoped lookups.
CREATE INDEX IF NOT EXISTS idx_saved_queries_org ON app.saved_queries(organization_id);
CREATE INDEX IF NOT EXISTS idx_reports_org ON app.reports(organization_id);
CREATE INDEX IF NOT EXISTS idx_schedules_org ON app.schedules(organization_id);
CREATE INDEX IF NOT EXISTS idx_dashboards_org ON app.dashboards(organization_id);
CREATE INDEX IF NOT EXISTS idx_ask_sessions_org ON app.ask_sessions(organization_id);
CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_retry
    ON app.webhook_deliveries(status, completed_at)
    WHERE status IN ('pending', 'failed');
CREATE INDEX IF NOT EXISTS idx_schedule_runs_lease
    ON app.schedule_runs(status, lease_until)
    WHERE status = 'running';

-- LLM budget ledger (daily aggregates per org).
CREATE TABLE IF NOT EXISTS app.llm_budget_usage (
    organization_id UUID NOT NULL REFERENCES app.organizations(id) ON DELETE CASCADE,
    usage_date DATE NOT NULL DEFAULT (CURRENT_DATE),
    prompt_tokens BIGINT NOT NULL DEFAULT 0,
    completion_tokens BIGINT NOT NULL DEFAULT 0,
    estimated_cost_usd NUMERIC(14, 6) NOT NULL DEFAULT 0,
    call_count BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (organization_id, usage_date)
);

GRANT SELECT, INSERT, UPDATE ON app.llm_budget_usage TO pgquerynarrative_app;

-- Ensure app role cannot bypass RLS.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'pgquerynarrative_app') THEN
        ALTER ROLE pgquerynarrative_app NOSUPERUSER NOBYPASSRLS;
    END IF;
END $$;
