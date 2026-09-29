-- Durable buffer for audit.Store's "buffered" mode: entries that could not be written to
-- app.audit_logs immediately (queue full, or the write itself failed) are persisted here and
-- replayed by a background worker (Store.ReplayBuffered), so no entry is silently dropped
-- even across process restarts.
CREATE TABLE IF NOT EXISTS app.audit_log_buffer (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    event_type  TEXT NOT NULL,
    entity_type TEXT,
    entity_id   UUID,
    details     JSONB,
    user_id     TEXT,
    ip_address  INET,
    user_agent  TEXT,
    organization_id UUID,
    attempts    INT NOT NULL DEFAULT 0,
    last_error  TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_audit_log_buffer_created_at ON app.audit_log_buffer(created_at);

GRANT SELECT, INSERT, UPDATE, DELETE ON app.audit_log_buffer TO pgquerynarrative_app;
