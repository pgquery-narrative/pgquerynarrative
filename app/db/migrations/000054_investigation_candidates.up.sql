-- Keep every candidate tested for an investigation instead of overwriting the
-- last one. The investigations row keeps candidate_sql / candidate_explain /
-- comparison as the "current" pointer; this table is the history.

CREATE TABLE IF NOT EXISTS app.investigation_candidates (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES app.organizations(id) ON DELETE CASCADE,
    investigation_id UUID NOT NULL REFERENCES app.investigations(id) ON DELETE CASCADE,
    candidate_sql TEXT NOT NULL,
    binds JSONB,
    candidate_explain JSONB,
    comparison JSONB,
    equivalence_status TEXT,
    cost_delta DOUBLE PRECISION,
    source TEXT NOT NULL DEFAULT 'manual'
        CHECK (source IN ('manual', 'suggested', 'ranked')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_investigation_candidates_inv
    ON app.investigation_candidates (organization_id, investigation_id, updated_at DESC);

-- One row per distinct candidate SQL per investigation (re-testing updates it).
CREATE UNIQUE INDEX IF NOT EXISTS idx_investigation_candidates_unique
    ON app.investigation_candidates (investigation_id, md5(candidate_sql));

ALTER TABLE app.investigation_candidates ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.investigation_candidates FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS investigation_candidates_org ON app.investigation_candidates;
CREATE POLICY investigation_candidates_org ON app.investigation_candidates
    USING (organization_id::text = NULLIF(current_setting('app.current_org_id', true), ''));

GRANT SELECT, INSERT, UPDATE, DELETE ON app.investigation_candidates TO pgquerynarrative_app;
