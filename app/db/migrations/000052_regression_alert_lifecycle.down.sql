DROP INDEX IF EXISTS app.idx_stat_statement_polls_org_captured_asc;
DROP INDEX IF EXISTS app.idx_stat_statement_snapshots_queryid;
DROP INDEX IF EXISTS app.idx_regression_alerts_open_queryid;

CREATE INDEX IF NOT EXISTS idx_regression_alerts_queryid
    ON app.regression_alerts (organization_id, queryid)
    WHERE acknowledged_at IS NULL AND queryid IS NOT NULL;

ALTER TABLE app.regression_alerts
    DROP COLUMN IF EXISTS previous_alert_id,
    DROP COLUMN IF EXISTS baseline_mean_time_ms,
    DROP COLUMN IF EXISTS occurrences,
    DROP COLUMN IF EXISTS last_seen_at,
    DROP COLUMN IF EXISTS resolved_at;
