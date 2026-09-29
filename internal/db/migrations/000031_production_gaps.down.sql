DROP POLICY IF EXISTS explain_snapshots_org ON app.explain_snapshots;
DROP TABLE IF EXISTS app.explain_snapshots;

DROP POLICY IF EXISTS llm_audit_events_org ON app.llm_audit_events;
CREATE POLICY llm_audit_events_org ON app.llm_audit_events
    USING (
        organization_id::text = NULLIF(current_setting('app.current_org_id', true), '')
        OR current_setting('app.scheduler_bypass', true) = 'true'
    );

DROP POLICY IF EXISTS llm_budget_usage_org ON app.llm_budget_usage;
ALTER TABLE app.llm_budget_usage DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS report_embeddings_org ON app.report_embeddings;
ALTER TABLE app.report_embeddings DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS query_embeddings_org ON app.query_embeddings;
ALTER TABLE app.query_embeddings DISABLE ROW LEVEL SECURITY;

DROP TABLE IF EXISTS app.oidc_group_org_mappings;
DROP TABLE IF EXISTS app.oidc_pkce_states;
