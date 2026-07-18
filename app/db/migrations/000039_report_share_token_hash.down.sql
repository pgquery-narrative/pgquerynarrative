DROP FUNCTION IF EXISTS app.resolve_report_share_token(text);

DROP INDEX IF EXISTS app.idx_report_share_tokens_token_hash;
DROP INDEX IF EXISTS app.idx_report_share_tokens_report_id;

-- Best effort: restores the original token-based lookup function. If multiple
-- rows now exist per report_id, the original report_id PRIMARY KEY cannot be
-- restored without manually deduplicating first.
ALTER TABLE app.report_share_tokens DROP CONSTRAINT IF EXISTS report_share_tokens_pkey;
ALTER TABLE app.report_share_tokens ADD PRIMARY KEY (report_id);

ALTER TABLE app.report_share_tokens ALTER COLUMN token SET NOT NULL;
ALTER TABLE app.report_share_tokens ADD CONSTRAINT report_share_tokens_token_key UNIQUE (token);
CREATE INDEX IF NOT EXISTS idx_report_share_tokens_token ON app.report_share_tokens(token);

ALTER TABLE app.report_share_tokens
    DROP COLUMN IF EXISTS id,
    DROP COLUMN IF EXISTS token_hash,
    DROP COLUMN IF EXISTS access_count,
    DROP COLUMN IF EXISTS last_accessed_at,
    DROP COLUMN IF EXISTS revoked_at;

CREATE OR REPLACE FUNCTION app.resolve_report_share_token(p_token text)
RETURNS TABLE(report_id uuid, organization_id uuid)
LANGUAGE sql
SECURITY DEFINER
SET search_path = app, pg_temp
AS $$
    SELECT t.report_id, t.organization_id
    FROM app.report_share_tokens t
    WHERE t.token = p_token
      AND (t.expires_at IS NULL OR t.expires_at > NOW());
$$;
REVOKE ALL ON FUNCTION app.resolve_report_share_token(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.resolve_report_share_token(text) TO pgquerynarrative_app;
