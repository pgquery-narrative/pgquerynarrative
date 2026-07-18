ALTER TABLE app.explain_snapshots
    DROP CONSTRAINT IF EXISTS explain_snapshots_sql_storage_class_check;
ALTER TABLE app.explain_snapshots
    ADD CONSTRAINT explain_snapshots_sql_storage_class_check
    CHECK (sql_storage_class IN ('raw', 'redacted', 'fingerprint'));

DROP FUNCTION IF EXISTS app.resolve_managed_api_key(text);
DROP POLICY IF EXISTS managed_api_keys_org ON app.managed_api_keys;
DROP TABLE IF EXISTS app.managed_api_keys;
