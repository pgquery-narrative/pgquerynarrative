-- Drop 'encrypted' from the allowed storage classes.
--
-- NOT VALID for the same reason as 000008: a deployment with SQL encryption
-- enabled has rows marked 'encrypted', and a validated CHECK would refuse to be
-- re-added, blocking the rollback. Rewriting those rows to 'raw' would be worse
-- than blocking it — the column would then claim the stored value is plaintext
-- SQL when it is still ciphertext, and the pre-000042 reader would hand that
-- ciphertext back as if it were a query. Leave the label truthful and enforce
-- the narrower set only on new writes.
ALTER TABLE app.explain_snapshots
    DROP CONSTRAINT IF EXISTS explain_snapshots_sql_storage_class_check;
ALTER TABLE app.explain_snapshots
    ADD CONSTRAINT explain_snapshots_sql_storage_class_check
    CHECK (sql_storage_class IN ('raw', 'redacted', 'fingerprint')) NOT VALID;

DROP FUNCTION IF EXISTS app.resolve_managed_api_key(text);
DROP POLICY IF EXISTS managed_api_keys_org ON app.managed_api_keys;
DROP TABLE IF EXISTS app.managed_api_keys;
