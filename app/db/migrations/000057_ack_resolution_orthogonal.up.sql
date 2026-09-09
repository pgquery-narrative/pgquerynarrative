-- Acknowledgement and resolution are independent facts about a regression alert:
--
--   acknowledged_at = an analyst has seen this alert
--   resolved_at     = the query's performance returned to baseline
--
-- 000055 conflated them. Its partial unique index covered only alerts that were
-- BOTH unresolved and unacknowledged, which produced two lifecycle anomalies:
--
--   1. Acknowledging an alert dropped it out of the uniqueness predicate, so the
--      next poll happily inserted a *second* open alert for the same query
--      instead of bumping the existing one's occurrence count.
--   2. resolveRecoveredAlerts only ever considered unacknowledged alerts, so an
--      acknowledged alert stayed open forever even after the query recovered.
--
-- Uniqueness and recovery both belong to resolution alone. Acknowledgement is a
-- read-state flag and must not affect either.

-- Collapse duplicate open alerts the old predicate allowed to accumulate. Keep
-- the most recently detected one per (organization, connection, query); the rest
-- are resolved so the stricter index below can be created.
WITH ranked AS (
    SELECT id,
           row_number() OVER (
               PARTITION BY organization_id, connection_id, queryid
               ORDER BY first_detected_at DESC
           ) AS rn
    FROM app.regression_alerts
    WHERE resolved_at IS NULL AND queryid IS NOT NULL
)
UPDATE app.regression_alerts SET resolved_at = now()
WHERE id IN (SELECT id FROM ranked WHERE rn > 1);

DROP INDEX IF EXISTS app.idx_regression_alerts_one_open;

CREATE UNIQUE INDEX IF NOT EXISTS idx_regression_alerts_one_open
    ON app.regression_alerts (organization_id, connection_id, queryid)
    WHERE resolved_at IS NULL AND queryid IS NOT NULL;

-- Rank an alert's impact so the poller can tell escalation from a steady
-- regression. Acknowledgement keeps a handled alert out of the inbox, but since
-- one alert now absorbs every later detection for the query, an alert that gets
-- substantially worse after being acknowledged must be surfaced again.
CREATE OR REPLACE FUNCTION app.regression_impact_rank(impact TEXT)
RETURNS INTEGER
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
AS $$
    SELECT CASE lower(coalesce(impact, ''))
        WHEN 'critical' THEN 3
        WHEN 'high'     THEN 2
        ELSE 1
    END
$$;

GRANT EXECUTE ON FUNCTION app.regression_impact_rank(TEXT) TO pgquerynarrative_app;
