-- Per-user LLM budget ledger (daily aggregates per org + user).

CREATE TABLE IF NOT EXISTS app.llm_user_budget_usage (
    organization_id UUID NOT NULL REFERENCES app.organizations(id) ON DELETE CASCADE,
    user_id TEXT NOT NULL,
    usage_date DATE NOT NULL DEFAULT (CURRENT_DATE),
    prompt_tokens BIGINT NOT NULL DEFAULT 0,
    completion_tokens BIGINT NOT NULL DEFAULT 0,
    estimated_cost_usd NUMERIC(14, 6) NOT NULL DEFAULT 0,
    call_count BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (organization_id, user_id, usage_date)
);

CREATE INDEX IF NOT EXISTS idx_llm_user_budget_usage_month
    ON app.llm_user_budget_usage (organization_id, user_id, usage_date DESC);

GRANT SELECT, INSERT, UPDATE ON app.llm_user_budget_usage TO pgquerynarrative_app;

ALTER TABLE app.llm_user_budget_usage ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.llm_user_budget_usage FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS llm_user_budget_usage_org ON app.llm_user_budget_usage;
CREATE POLICY llm_user_budget_usage_org ON app.llm_user_budget_usage
    USING (
        organization_id::text = NULLIF(current_setting('app.current_org_id', true), '')
        OR current_setting('app.scheduler_bypass', true) = 'true'
    );
