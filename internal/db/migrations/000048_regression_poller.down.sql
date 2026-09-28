ALTER TABLE app.regression_alerts DROP COLUMN IF EXISTS queryid;
ALTER TABLE app.regression_alerts DROP COLUMN IF EXISTS source;
DROP TABLE IF EXISTS app.stat_statement_snapshots;
DROP TABLE IF EXISTS app.stat_statement_polls;
