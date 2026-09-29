CREATE TABLE IF NOT EXISTS app.ask_sessions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    connection_id TEXT NOT NULL DEFAULT 'default',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS app.ask_messages (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id UUID NOT NULL REFERENCES app.ask_sessions(id) ON DELETE CASCADE,
    question TEXT NOT NULL,
    sql TEXT NOT NULL,
    report_id UUID REFERENCES app.reports(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_ask_messages_session_created ON app.ask_messages(session_id, created_at DESC);
