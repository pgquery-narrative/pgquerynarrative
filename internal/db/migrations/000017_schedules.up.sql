CREATE TABLE IF NOT EXISTS app.schedules (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,
    saved_query_id UUID REFERENCES app.saved_queries(id) ON DELETE SET NULL,
    sql TEXT,
    connection_id TEXT NOT NULL DEFAULT 'default',
    cron_expr TEXT NOT NULL,
    destination_type TEXT NOT NULL DEFAULT 'webhook',
    destination_target TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    last_run_at TIMESTAMPTZ,
    last_status TEXT,
    last_error TEXT,
    next_run_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_schedules_enabled_next_run ON app.schedules(enabled, next_run_at);
