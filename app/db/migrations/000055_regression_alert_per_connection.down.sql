DROP INDEX IF EXISTS app.idx_stat_statement_polls_org_conn_captured;
DROP INDEX IF EXISTS app.idx_regression_alerts_one_open;

-- Restore the plain partial index from 000052.
CREATE INDEX IF NOT EXISTS idx_regression_alerts_open_queryid
    ON app.regression_alerts (organization_id, queryid)
    WHERE acknowledged_at IS NULL AND resolved_at IS NULL AND queryid IS NOT NULL;
