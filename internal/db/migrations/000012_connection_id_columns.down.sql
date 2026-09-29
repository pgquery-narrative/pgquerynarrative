DROP INDEX IF EXISTS idx_reports_connection_id;
DROP INDEX IF EXISTS idx_saved_queries_connection_id;

ALTER TABLE app.reports
DROP COLUMN IF EXISTS connection_id;

ALTER TABLE app.saved_queries
DROP COLUMN IF EXISTS connection_id;
