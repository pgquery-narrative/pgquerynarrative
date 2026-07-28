-- Periodic pg_stat_statements snapshots for regression detection.

CREATE TABLE IF NOT EXISTS app.stat_statement_polls (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES app.organizations(id) ON DELETE CASCADE,
    connection_id TEXT NOT NULL DEFAULT 'default',
    captured_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_stat_statement_polls_org_captured
    ON app.stat_statement_polls (organization_id, captured_at DESC);

CREATE TABLE IF NOT EXISTS app.stat_statement_snapshots (
    poll_id UUID NOT NULL REFERENCES app.stat_statement_polls(id) ON DELETE CASCADE,
    queryid TEXT NOT NULL,
    query_text TEXT NOT NULL,
    calls BIGINT NOT NULL DEFAULT 0,
    mean_time_ms DOUBLE PRECISION NOT NULL DEFAULT 0,
    total_time_ms DOUBLE PRECISION NOT NULL DEFAULT 0,
    rows BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (poll_id, queryid)
);

ALTER TABLE app.stat_statement_polls ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.stat_statement_polls FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS stat_statement_polls_org ON app.stat_statement_polls;
CREATE POLICY stat_statement_polls_org ON app.stat_statement_polls
    USING (organization_id::text = NULLIF(current_setting('app.current_org_id', true), ''));

ALTER TABLE app.stat_statement_snapshots ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.stat_statement_snapshots FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS stat_statement_snapshots_org ON app.stat_statement_snapshots;
CREATE POLICY stat_statement_snapshots_org ON app.stat_statement_snapshots
    USING (
        EXISTS (
            SELECT 1 FROM app.stat_statement_polls p
            WHERE p.id = poll_id
              AND p.organization_id::text = NULLIF(current_setting('app.current_org_id', true), '')
        )
    );

GRANT SELECT, INSERT, DELETE ON app.stat_statement_polls TO pgquerynarrative_app;
GRANT SELECT, INSERT, DELETE ON app.stat_statement_snapshots TO pgquerynarrative_app;

-- Distinguish demo-seeded alerts from poller-detected regressions.
ALTER TABLE app.regression_alerts
    ADD COLUMN IF NOT EXISTS source TEXT NOT NULL DEFAULT 'poller'
        CHECK (source IN ('poller', 'demo'));

ALTER TABLE app.regression_alerts
    ADD COLUMN IF NOT EXISTS queryid TEXT;

CREATE INDEX IF NOT EXISTS idx_regression_alerts_queryid
    ON app.regression_alerts (organization_id, queryid)
    WHERE acknowledged_at IS NULL AND queryid IS NOT NULL;
