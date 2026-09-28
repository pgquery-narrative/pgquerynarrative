-- One open regression alert per (organization, connection, query), enforced by
-- the database. A partial UNIQUE index replaces the plain partial index from
-- 000052 so two poller replicas cannot both insert an alert for the same query.

-- Resolve any pre-existing duplicate open alerts (keep the most recently detected).
WITH ranked AS (
    SELECT id,
           row_number() OVER (
               PARTITION BY organization_id, connection_id, queryid
               ORDER BY first_detected_at DESC
           ) AS rn
    FROM app.regression_alerts
    WHERE resolved_at IS NULL AND acknowledged_at IS NULL AND queryid IS NOT NULL
)
UPDATE app.regression_alerts SET resolved_at = now()
WHERE id IN (SELECT id FROM ranked WHERE rn > 1);

DROP INDEX IF EXISTS app.idx_regression_alerts_open_queryid;

CREATE UNIQUE INDEX IF NOT EXISTS idx_regression_alerts_one_open
    ON app.regression_alerts (organization_id, connection_id, queryid)
    WHERE resolved_at IS NULL AND acknowledged_at IS NULL AND queryid IS NOT NULL;

-- The interval-delta detection query scans snapshots per connection over a
-- captured_at window.
CREATE INDEX IF NOT EXISTS idx_stat_statement_polls_org_conn_captured
    ON app.stat_statement_polls (organization_id, connection_id, captured_at);
