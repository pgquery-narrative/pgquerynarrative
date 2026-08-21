-- Link regression alerts to investigations opened from the inbox.
-- Optionally enable hypopg when the extension package is installed (skipped otherwise).

ALTER TABLE app.regression_alerts
    ADD COLUMN IF NOT EXISTS investigation_id UUID REFERENCES app.investigations(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_regression_alerts_investigation
    ON app.regression_alerts (organization_id, investigation_id)
    WHERE investigation_id IS NOT NULL;

-- Snapshot stats on the alert itself so Open Investigation works without a join.
ALTER TABLE app.regression_alerts
    ADD COLUMN IF NOT EXISTS calls BIGINT,
    ADD COLUMN IF NOT EXISTS mean_time_ms DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS total_time_ms DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS rows_count BIGINT;

DO $$
BEGIN
  CREATE EXTENSION IF NOT EXISTS hypopg;
EXCEPTION
  WHEN undefined_file THEN
    RAISE NOTICE 'hypopg extension package not installed — projected index cost will use heuristic fallback';
  WHEN insufficient_privilege THEN
    RAISE NOTICE 'skip CREATE EXTENSION hypopg (need superuser migrate)';
  WHEN OTHERS THEN
    RAISE NOTICE 'skip CREATE EXTENSION hypopg: %', SQLERRM;
END $$;
