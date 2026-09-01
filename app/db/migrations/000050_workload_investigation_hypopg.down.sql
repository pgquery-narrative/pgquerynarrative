ALTER TABLE app.regression_alerts DROP COLUMN IF EXISTS rows_count;
ALTER TABLE app.regression_alerts DROP COLUMN IF EXISTS total_time_ms;
ALTER TABLE app.regression_alerts DROP COLUMN IF EXISTS mean_time_ms;
ALTER TABLE app.regression_alerts DROP COLUMN IF EXISTS calls;
DROP INDEX IF EXISTS app.idx_regression_alerts_investigation;
ALTER TABLE app.regression_alerts DROP COLUMN IF EXISTS investigation_id;
-- Leave hypopg installed if present (safe); do not DROP EXTENSION in down migration.
