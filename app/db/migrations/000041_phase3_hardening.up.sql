-- Phase 3 hardening: drop legacy share-token plaintext, durable API-key last-used,
-- and classify EXPLAIN snapshot SQL storage.

-- 1) Clear and drop plaintext share tokens (hashes already backfilled in 000039).
UPDATE app.report_share_tokens SET token = NULL WHERE token IS NOT NULL;
ALTER TABLE app.report_share_tokens DROP COLUMN IF EXISTS token;

-- 2) Durable last-used tracking for managed API keys (survives restart / multi-replica).
CREATE TABLE IF NOT EXISTS app.api_key_usage (
    key_id TEXT PRIMARY KEY,
    last_used_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    use_count BIGINT NOT NULL DEFAULT 0
);
REVOKE ALL ON TABLE app.api_key_usage FROM PUBLIC;
GRANT SELECT, INSERT, UPDATE ON TABLE app.api_key_usage TO pgquerynarrative_app;

-- 3) Explicit storage classification on EXPLAIN snapshots (fingerprint vs redacted).
-- Encryption of recovery-critical raw SQL/plans is deferred; retention (000040) bounds exposure.
ALTER TABLE app.explain_snapshots
    ADD COLUMN IF NOT EXISTS sql_storage_class TEXT NOT NULL DEFAULT 'redacted';

ALTER TABLE app.explain_snapshots
    DROP CONSTRAINT IF EXISTS explain_snapshots_sql_storage_class_check;
ALTER TABLE app.explain_snapshots
    ADD CONSTRAINT explain_snapshots_sql_storage_class_check
    CHECK (sql_storage_class IN ('raw', 'redacted', 'fingerprint'));
