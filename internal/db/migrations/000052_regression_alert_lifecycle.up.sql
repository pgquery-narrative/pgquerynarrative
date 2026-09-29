-- Regression alert lifecycle: recovery (auto-resolve), recurrence tracking, and
-- a recorded baseline so the inbox can show "N occurrences since <date>".

ALTER TABLE app.regression_alerts
    ADD COLUMN IF NOT EXISTS resolved_at            TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS last_seen_at           TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS occurrences            INTEGER NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS baseline_mean_time_ms  DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS previous_alert_id      UUID REFERENCES app.regression_alerts(id) ON DELETE SET NULL;

-- Back-fill last_seen_at for rows that predate this column.
UPDATE app.regression_alerts SET last_seen_at = first_detected_at WHERE last_seen_at IS NULL;

-- The "one open alert per query" guard must also ignore resolved rows, so a
-- query that recovered and then regressed again opens a fresh alert.
DROP INDEX IF EXISTS app.idx_regression_alerts_queryid;
CREATE INDEX IF NOT EXISTS idx_regression_alerts_open_queryid
    ON app.regression_alerts (organization_id, queryid)
    WHERE acknowledged_at IS NULL AND resolved_at IS NULL AND queryid IS NOT NULL;

-- Baseline windowing scans snapshots by queryid across a time range of polls.
CREATE INDEX IF NOT EXISTS idx_stat_statement_snapshots_queryid
    ON app.stat_statement_snapshots (queryid);
CREATE INDEX IF NOT EXISTS idx_stat_statement_polls_org_captured_asc
    ON app.stat_statement_polls (organization_id, captured_at);
