-- Production readiness: organizations, durable schedule runs, webhook delivery audit,
-- LLM governance audit, and organization scoping on app metadata tables.

CREATE TABLE IF NOT EXISTS app.organizations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,
    slug TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO app.organizations (id, name, slug)
VALUES ('00000000-0000-0000-0000-000000000001', 'Default Organization', 'default')
ON CONFLICT (slug) DO NOTHING;

CREATE TABLE IF NOT EXISTS app.organization_members (
    organization_id UUID NOT NULL REFERENCES app.organizations(id) ON DELETE CASCADE,
    user_id TEXT NOT NULL,
    role TEXT NOT NULL DEFAULT 'analyst',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (organization_id, user_id),
    CONSTRAINT organization_members_role_check CHECK (role IN ('admin', 'analyst', 'viewer'))
);

ALTER TABLE app.saved_queries
    ADD COLUMN IF NOT EXISTS organization_id UUID REFERENCES app.organizations(id);
ALTER TABLE app.reports
    ADD COLUMN IF NOT EXISTS organization_id UUID REFERENCES app.organizations(id);
ALTER TABLE app.schedules
    ADD COLUMN IF NOT EXISTS organization_id UUID REFERENCES app.organizations(id);
ALTER TABLE app.dashboards
    ADD COLUMN IF NOT EXISTS organization_id UUID REFERENCES app.organizations(id);
ALTER TABLE app.ask_sessions
    ADD COLUMN IF NOT EXISTS organization_id UUID REFERENCES app.organizations(id);

UPDATE app.saved_queries SET organization_id = '00000000-0000-0000-0000-000000000001' WHERE organization_id IS NULL;
UPDATE app.reports SET organization_id = '00000000-0000-0000-0000-000000000001' WHERE organization_id IS NULL;
UPDATE app.schedules SET organization_id = '00000000-0000-0000-0000-000000000001' WHERE organization_id IS NULL;
UPDATE app.dashboards SET organization_id = '00000000-0000-0000-0000-000000000001' WHERE organization_id IS NULL;
UPDATE app.ask_sessions SET organization_id = '00000000-0000-0000-0000-000000000001' WHERE organization_id IS NULL;

ALTER TABLE app.schedules
    ADD COLUMN IF NOT EXISTS created_by TEXT;

ALTER TABLE app.schedules
    ADD COLUMN IF NOT EXISTS locked_by TEXT,
    ADD COLUMN IF NOT EXISTS locked_until TIMESTAMPTZ;

CREATE TABLE IF NOT EXISTS app.schedule_runs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    schedule_id UUID NOT NULL REFERENCES app.schedules(id) ON DELETE CASCADE,
    organization_id UUID NOT NULL REFERENCES app.organizations(id),
    scheduled_for TIMESTAMPTZ NOT NULL,
    idempotency_key TEXT NOT NULL UNIQUE,
    worker_id TEXT,
    lease_until TIMESTAMPTZ,
    status TEXT NOT NULL DEFAULT 'pending',
    attempt_count INT NOT NULL DEFAULT 0,
    report_id UUID REFERENCES app.reports(id) ON DELETE SET NULL,
    failure_code TEXT,
    failure_message TEXT,
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT schedule_runs_status_check CHECK (status IN (
        'pending', 'running', 'completed', 'failed', 'dead_letter'
    ))
);

CREATE INDEX IF NOT EXISTS idx_schedule_runs_schedule ON app.schedule_runs(schedule_id, scheduled_for DESC);
CREATE INDEX IF NOT EXISTS idx_schedule_runs_status ON app.schedule_runs(status, lease_until);

CREATE TABLE IF NOT EXISTS app.webhook_deliveries (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES app.organizations(id),
    schedule_id UUID REFERENCES app.schedules(id) ON DELETE SET NULL,
    schedule_run_id UUID REFERENCES app.schedule_runs(id) ON DELETE SET NULL,
    destination_url TEXT NOT NULL,
    idempotency_key TEXT NOT NULL UNIQUE,
    payload JSONB NOT NULL,
    signature TEXT,
    status TEXT NOT NULL DEFAULT 'pending',
    attempt_count INT NOT NULL DEFAULT 0,
    http_status INT,
    response_bytes INT,
    error_message TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ,
    CONSTRAINT webhook_deliveries_status_check CHECK (status IN (
        'pending', 'delivered', 'failed', 'dead_letter'
    ))
);

CREATE TABLE IF NOT EXISTS app.llm_audit_events (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES app.organizations(id),
    user_id TEXT,
    provider TEXT NOT NULL,
    model TEXT NOT NULL,
    operation TEXT NOT NULL,
    policy_decision TEXT NOT NULL,
    data_classes TEXT[] NOT NULL DEFAULT '{}',
    send_row_data BOOLEAN NOT NULL DEFAULT false,
    redact_pii BOOLEAN NOT NULL DEFAULT true,
    allow_external BOOLEAN NOT NULL DEFAULT false,
    prompt_tokens INT,
    completion_tokens INT,
    estimated_cost_usd NUMERIC(12, 6),
    latency_ms INT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_llm_audit_org_created ON app.llm_audit_events(organization_id, created_at DESC);

-- RLS: defense in depth when app.current_org_id is set on the session.
ALTER TABLE app.saved_queries ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.reports ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.schedules ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.dashboards ENABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS saved_queries_org ON app.saved_queries;
CREATE POLICY saved_queries_org ON app.saved_queries
    USING (
        organization_id::text = current_setting('app.current_org_id', true)
        OR current_setting('app.current_org_id', true) = ''
    );

DROP POLICY IF EXISTS reports_org ON app.reports;
CREATE POLICY reports_org ON app.reports
    USING (
        organization_id::text = current_setting('app.current_org_id', true)
        OR current_setting('app.current_org_id', true) = ''
    );

DROP POLICY IF EXISTS schedules_org ON app.schedules;
CREATE POLICY schedules_org ON app.schedules
    USING (
        organization_id::text = current_setting('app.current_org_id', true)
        OR current_setting('app.current_org_id', true) = ''
    );

DROP POLICY IF EXISTS dashboards_org ON app.dashboards;
CREATE POLICY dashboards_org ON app.dashboards
    USING (
        organization_id::text = current_setting('app.current_org_id', true)
        OR current_setting('app.current_org_id', true) = ''
    );

GRANT SELECT, INSERT, UPDATE, DELETE ON app.organizations TO pgquerynarrative_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON app.organization_members TO pgquerynarrative_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON app.schedule_runs TO pgquerynarrative_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON app.webhook_deliveries TO pgquerynarrative_app;
GRANT SELECT, INSERT ON app.llm_audit_events TO pgquerynarrative_app;
