ALTER TABLE app.saved_queries
ADD COLUMN IF NOT EXISTS connection_id TEXT NOT NULL DEFAULT 'default';

ALTER TABLE app.reports
ADD COLUMN IF NOT EXISTS connection_id TEXT NOT NULL DEFAULT 'default';

CREATE INDEX IF NOT EXISTS idx_saved_queries_connection_id ON app.saved_queries(connection_id);
CREATE INDEX IF NOT EXISTS idx_reports_connection_id ON app.reports(connection_id);
