CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE IF NOT EXISTS app.saved_queries (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,
    sql TEXT NOT NULL,
    description TEXT,
    tags TEXT[] DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_by TEXT,
    CONSTRAINT saved_queries_name_check CHECK (char_length(name) > 0),
    CONSTRAINT saved_queries_sql_check CHECK (char_length(sql) > 0)
);

CREATE INDEX IF NOT EXISTS idx_saved_queries_tags ON app.saved_queries USING GIN(tags);
CREATE INDEX IF NOT EXISTS idx_saved_queries_created_at ON app.saved_queries(created_at DESC);

CREATE TABLE IF NOT EXISTS app.reports (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    saved_query_id UUID REFERENCES app.saved_queries(id) ON DELETE SET NULL,
    sql TEXT NOT NULL,
    result_hash TEXT,
    narrative_md TEXT NOT NULL,
    narrative_json JSONB NOT NULL,
    metrics JSONB NOT NULL,
    stats JSONB,
    llm_model TEXT NOT NULL,
    llm_provider TEXT NOT NULL,
    llm_tokens_used INT,
    llm_response_time_ms INT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_by TEXT,
    success BOOLEAN NOT NULL DEFAULT true,
    error TEXT,
    CONSTRAINT reports_sql_check CHECK (char_length(sql) > 0)
);

CREATE INDEX IF NOT EXISTS idx_reports_saved_query_id ON app.reports(saved_query_id);
CREATE INDEX IF NOT EXISTS idx_reports_result_hash ON app.reports(result_hash);
CREATE INDEX IF NOT EXISTS idx_reports_created_at ON app.reports(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_reports_llm_model ON app.reports(llm_model);

CREATE TABLE IF NOT EXISTS app.audit_logs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    event_type TEXT NOT NULL,
    entity_type TEXT,
    entity_id UUID,
    details JSONB,
    user_id TEXT,
    ip_address INET,
    user_agent TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT audit_logs_event_type_check CHECK (event_type IN (
        'RUN_QUERY', 'GENERATE_REPORT', 'EXPORT_REPORT',
        'SAVE_QUERY', 'DELETE_QUERY', 'UPDATE_QUERY',
        'AUTH_FAILURE', 'AUTH_SUCCESS', 'RATE_LIMIT_EXCEEDED',
        'INVALID_SQL_ATTEMPT', 'UNAUTHORIZED_ACCESS'
    ))
);

CREATE INDEX IF NOT EXISTS idx_audit_logs_event_type ON app.audit_logs(event_type);
CREATE INDEX IF NOT EXISTS idx_audit_logs_entity ON app.audit_logs(entity_type, entity_id);
CREATE INDEX IF NOT EXISTS idx_audit_logs_created_at ON app.audit_logs(created_at DESC);
