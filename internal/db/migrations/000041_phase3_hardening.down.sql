ALTER TABLE app.explain_snapshots DROP CONSTRAINT IF EXISTS explain_snapshots_sql_storage_class_check;
ALTER TABLE app.explain_snapshots DROP COLUMN IF EXISTS sql_storage_class;

DROP TABLE IF EXISTS app.api_key_usage;

ALTER TABLE app.report_share_tokens
    ADD COLUMN IF NOT EXISTS token TEXT;
