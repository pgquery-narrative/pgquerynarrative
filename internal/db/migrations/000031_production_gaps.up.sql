-- Production gaps: multi-replica OIDC PKCE, group→org mapping, embedding RLS, explain history.

CREATE TABLE IF NOT EXISTS app.oidc_pkce_states (
    state TEXT PRIMARY KEY,
    verifier TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_oidc_pkce_expires ON app.oidc_pkce_states(expires_at);

GRANT SELECT, INSERT, DELETE ON app.oidc_pkce_states TO pgquerynarrative_app;

CREATE TABLE IF NOT EXISTS app.oidc_group_org_mappings (
    group_claim TEXT PRIMARY KEY,
    organization_id UUID NOT NULL REFERENCES app.organizations(id) ON DELETE CASCADE,
    role TEXT NOT NULL DEFAULT 'analyst',
    CONSTRAINT oidc_group_org_mappings_role_check CHECK (role IN ('admin', 'analyst', 'viewer'))
);

GRANT SELECT, INSERT, UPDATE, DELETE ON app.oidc_group_org_mappings TO pgquerynarrative_app;

-- Embedding tables inherit org isolation via parent saved_queries / reports.
ALTER TABLE app.query_embeddings ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.query_embeddings FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS query_embeddings_org ON app.query_embeddings;
CREATE POLICY query_embeddings_org ON app.query_embeddings
    USING (
        EXISTS (
            SELECT 1 FROM app.saved_queries sq
            WHERE sq.id = saved_query_id
              AND sq.organization_id::text = NULLIF(current_setting('app.current_org_id', true), '')
        )
    );

ALTER TABLE app.report_embeddings ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.report_embeddings FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS report_embeddings_org ON app.report_embeddings;
CREATE POLICY report_embeddings_org ON app.report_embeddings
    USING (
        EXISTS (
            SELECT 1 FROM app.reports r
            WHERE r.id = report_id
              AND r.organization_id::text = NULLIF(current_setting('app.current_org_id', true), '')
        )
    );

ALTER TABLE app.llm_budget_usage ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.llm_budget_usage FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS llm_budget_usage_org ON app.llm_budget_usage;
CREATE POLICY llm_budget_usage_org ON app.llm_budget_usage
    USING (
        organization_id::text = NULLIF(current_setting('app.current_org_id', true), '')
        OR current_setting('app.scheduler_bypass', true) = 'true'
    );

-- Allow governed LLM audit inserts when org context or scheduler bypass is set.
DROP POLICY IF EXISTS llm_audit_events_org ON app.llm_audit_events;
CREATE POLICY llm_audit_events_org ON app.llm_audit_events
    USING (
        organization_id::text = NULLIF(current_setting('app.current_org_id', true), '')
        OR current_setting('app.scheduler_bypass', true) = 'true'
    )
    WITH CHECK (
        organization_id::text = NULLIF(current_setting('app.current_org_id', true), '')
        OR current_setting('app.scheduler_bypass', true) = 'true'
    );

CREATE TABLE IF NOT EXISTS app.explain_snapshots (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES app.organizations(id),
    user_id TEXT,
    connection_id TEXT NOT NULL DEFAULT 'default',
    sql_hash TEXT NOT NULL,
    sql_text TEXT NOT NULL,
    used_analyze BOOLEAN NOT NULL DEFAULT false,
    total_cost DOUBLE PRECISION,
    findings JSONB NOT NULL DEFAULT '[]'::jsonb,
    explain_plan JSONB,
    execution_time_ms BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_explain_snapshots_org_created
    ON app.explain_snapshots(organization_id, created_at DESC);

ALTER TABLE app.explain_snapshots ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.explain_snapshots FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS explain_snapshots_org ON app.explain_snapshots;
CREATE POLICY explain_snapshots_org ON app.explain_snapshots
    USING (organization_id::text = NULLIF(current_setting('app.current_org_id', true), ''));

GRANT SELECT, INSERT ON app.explain_snapshots TO pgquerynarrative_app;
