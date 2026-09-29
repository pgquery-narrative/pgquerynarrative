CREATE TABLE IF NOT EXISTS app.report_share_tokens (
    report_id UUID PRIMARY KEY REFERENCES app.reports(id) ON DELETE CASCADE,
    token TEXT NOT NULL UNIQUE,
    expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_report_share_tokens_token ON app.report_share_tokens(token);
CREATE INDEX IF NOT EXISTS idx_report_share_tokens_expires_at ON app.report_share_tokens(expires_at);
