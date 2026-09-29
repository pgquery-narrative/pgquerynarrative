-- Atomic LLM budget reservation ledger: reserve tokens/cost before an LLM
-- provider call, reconcile to actual usage on success, release on failure,
-- and expire abandoned reservations left behind by crashed requests.

CREATE TABLE IF NOT EXISTS app.llm_budget_reservations (
    request_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES app.organizations(id) ON DELETE CASCADE,
    user_id TEXT NOT NULL DEFAULT '',
    reserved_tokens BIGINT NOT NULL DEFAULT 0,
    reserved_cost_usd NUMERIC(14, 6) NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'reserved', -- reserved, committed, released, expired
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT llm_budget_reservations_status_chk
        CHECK (status IN ('reserved', 'committed', 'released', 'expired'))
);

CREATE INDEX IF NOT EXISTS idx_llm_budget_reservations_active
    ON app.llm_budget_reservations (organization_id, user_id, status, expires_at);

GRANT SELECT, INSERT, UPDATE ON app.llm_budget_reservations TO pgquerynarrative_app;

ALTER TABLE app.llm_budget_reservations ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.llm_budget_reservations FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS llm_budget_reservations_org ON app.llm_budget_reservations;
CREATE POLICY llm_budget_reservations_org ON app.llm_budget_reservations
    USING (
        organization_id::text = NULLIF(current_setting('app.current_org_id', true), '')
        OR current_setting('app.scheduler_bypass', true) = 'true'
    );
