-- Query Investigation workflow and regression inbox.

CREATE TABLE IF NOT EXISTS app.investigations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES app.organizations(id) ON DELETE CASCADE,
    created_by TEXT,
    title TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'open'
        CHECK (status IN ('open', 'analyzing', 'comparing', 'complete')),
    sql TEXT NOT NULL,
    connection_id TEXT NOT NULL DEFAULT 'default',
    query_fingerprint TEXT,
    stat_snapshot JSONB,
    explain_result JSONB,
    candidate_sql TEXT,
    candidate_explain JSONB,
    comparison JSONB,
    report_id UUID REFERENCES app.reports(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_investigations_org_updated
    ON app.investigations (organization_id, updated_at DESC);

ALTER TABLE app.investigations ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.investigations FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS investigations_org ON app.investigations;
CREATE POLICY investigations_org ON app.investigations
    USING (organization_id::text = NULLIF(current_setting('app.current_org_id', true), ''));

GRANT SELECT, INSERT, UPDATE, DELETE ON app.investigations TO pgquerynarrative_app;

CREATE TABLE IF NOT EXISTS app.regression_alerts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES app.organizations(id) ON DELETE CASCADE,
    title TEXT NOT NULL,
    query_text TEXT NOT NULL,
    change_type TEXT NOT NULL
        CHECK (change_type IN ('latency', 'total_time', 'calls', 'temp_writes', 'plan_change', 'rows')),
    change_percent DOUBLE PRECISION,
    change_summary TEXT NOT NULL,
    impact TEXT NOT NULL
        CHECK (impact IN ('critical', 'high', 'medium', 'low')),
    connection_id TEXT NOT NULL DEFAULT 'default',
    first_detected_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    acknowledged_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_regression_alerts_org_detected
    ON app.regression_alerts (organization_id, first_detected_at DESC)
    WHERE acknowledged_at IS NULL;

ALTER TABLE app.regression_alerts ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.regression_alerts FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS regression_alerts_org ON app.regression_alerts;
CREATE POLICY regression_alerts_org ON app.regression_alerts
    USING (organization_id::text = NULLIF(current_setting('app.current_org_id', true), ''));

GRANT SELECT, INSERT, UPDATE, DELETE ON app.regression_alerts TO pgquerynarrative_app;
