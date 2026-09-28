-- Hash report share tokens instead of storing them in plaintext, track usage,
-- support revocation, and allow multiple active links per report.

ALTER TABLE app.report_share_tokens
    ADD COLUMN IF NOT EXISTS id UUID NOT NULL DEFAULT gen_random_uuid(),
    ADD COLUMN IF NOT EXISTS token_hash TEXT,
    ADD COLUMN IF NOT EXISTS access_count INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS last_accessed_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS revoked_at TIMESTAMPTZ;

-- Backfill token_hash for any pre-existing plaintext tokens (SHA-256 hex digest,
-- matching the application's sha256.Sum256 + hex.EncodeToString).
UPDATE app.report_share_tokens
SET token_hash = encode(digest(token, 'sha256'), 'hex')
WHERE token_hash IS NULL AND token IS NOT NULL;

-- Replace report_id as primary key with a surrogate id so a report can have
-- more than one active share link (e.g. rotated links, multiple recipients).
ALTER TABLE app.report_share_tokens DROP CONSTRAINT IF EXISTS report_share_tokens_pkey;
ALTER TABLE app.report_share_tokens ADD PRIMARY KEY (id);

-- Stop requiring/storing the plaintext token; new rows only carry the hash.
ALTER TABLE app.report_share_tokens ALTER COLUMN token DROP NOT NULL;
ALTER TABLE app.report_share_tokens DROP CONSTRAINT IF EXISTS report_share_tokens_token_key;
DROP INDEX IF EXISTS app.idx_report_share_tokens_token;

CREATE UNIQUE INDEX IF NOT EXISTS idx_report_share_tokens_token_hash
    ON app.report_share_tokens(token_hash) WHERE token_hash IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_report_share_tokens_report_id ON app.report_share_tokens(report_id);

-- Public share lookup: resolve by hash, enforce revocation, and record access
-- atomically in the same statement.
DROP FUNCTION IF EXISTS app.resolve_report_share_token(text);
CREATE FUNCTION app.resolve_report_share_token(p_token_hash text)
RETURNS TABLE(id uuid, report_id uuid, organization_id uuid)
LANGUAGE sql
SECURITY DEFINER
SET search_path = app, pg_temp
AS $$
    UPDATE app.report_share_tokens t
    SET access_count = t.access_count + 1,
        last_accessed_at = NOW()
    WHERE t.token_hash = p_token_hash
      AND t.revoked_at IS NULL
      AND (t.expires_at IS NULL OR t.expires_at > NOW())
    RETURNING t.id, t.report_id, t.organization_id;
$$;
REVOKE ALL ON FUNCTION app.resolve_report_share_token(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.resolve_report_share_token(text) TO pgquerynarrative_app;
