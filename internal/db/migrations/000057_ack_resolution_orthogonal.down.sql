DROP INDEX IF EXISTS app.idx_regression_alerts_one_open;

-- Restore the 000055 predicate, which also treated an acknowledged alert as
-- outside the uniqueness constraint.
CREATE UNIQUE INDEX IF NOT EXISTS idx_regression_alerts_one_open
    ON app.regression_alerts (organization_id, connection_id, queryid)
    WHERE resolved_at IS NULL AND acknowledged_at IS NULL AND queryid IS NOT NULL;

DROP FUNCTION IF EXISTS app.regression_impact_rank(TEXT);
