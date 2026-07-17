-- Child-table tenant isolation, share-token org column, audit org scoping,
-- and atomic webhook retry claim leases.

-- Denormalize organization onto share tokens for RLS + public resolve.
ALTER TABLE app.report_share_tokens
    ADD COLUMN IF NOT EXISTS organization_id UUID REFERENCES app.organizations(id);

UPDATE app.report_share_tokens t
SET organization_id = r.organization_id
FROM app.reports r
WHERE t.report_id = r.id
  AND t.organization_id IS NULL;

ALTER TABLE app.report_share_tokens
    ALTER COLUMN organization_id SET NOT NULL;

CREATE INDEX IF NOT EXISTS idx_report_share_tokens_org ON app.report_share_tokens(organization_id);

ALTER TABLE app.report_share_tokens ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.report_share_tokens FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS report_share_tokens_org ON app.report_share_tokens;
CREATE POLICY report_share_tokens_org ON app.report_share_tokens
    USING (organization_id::text = NULLIF(current_setting('app.current_org_id', true), ''));

-- Public share lookup bypasses RLS safely via SECURITY DEFINER.
CREATE OR REPLACE FUNCTION app.resolve_report_share_token(p_token text)
RETURNS TABLE(report_id uuid, organization_id uuid)
LANGUAGE sql
SECURITY DEFINER
SET search_path = app, pg_temp
AS $$
    SELECT t.report_id, t.organization_id
    FROM app.report_share_tokens t
    WHERE t.token = p_token
      AND (t.expires_at IS NULL OR t.expires_at > NOW());
$$;
REVOKE ALL ON FUNCTION app.resolve_report_share_token(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.resolve_report_share_token(text) TO pgquerynarrative_app;

-- ask_messages: inherit tenant from parent session.
ALTER TABLE app.ask_messages ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.ask_messages FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS ask_messages_org ON app.ask_messages;
CREATE POLICY ask_messages_org ON app.ask_messages
    USING (
        EXISTS (
            SELECT 1 FROM app.ask_sessions s
            WHERE s.id = ask_messages.session_id
              AND s.organization_id::text = NULLIF(current_setting('app.current_org_id', true), '')
        )
    );

-- dashboard_widgets: inherit tenant from parent dashboard.
ALTER TABLE app.dashboard_widgets ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.dashboard_widgets FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS dashboard_widgets_org ON app.dashboard_widgets;
CREATE POLICY dashboard_widgets_org ON app.dashboard_widgets
    USING (
        EXISTS (
            SELECT 1 FROM app.dashboards d
            WHERE d.id = dashboard_widgets.dashboard_id
              AND d.organization_id::text = NULLIF(current_setting('app.current_org_id', true), '')
        )
    );

-- audit_logs: org-scoped with fail-closed RLS.
ALTER TABLE app.audit_logs
    ADD COLUMN IF NOT EXISTS organization_id UUID REFERENCES app.organizations(id);

UPDATE app.audit_logs
SET organization_id = '00000000-0000-0000-0000-000000000001'
WHERE organization_id IS NULL;

CREATE INDEX IF NOT EXISTS idx_audit_logs_org_created ON app.audit_logs(organization_id, created_at DESC);

ALTER TABLE app.audit_logs ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.audit_logs FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS audit_logs_org ON app.audit_logs;
CREATE POLICY audit_logs_org ON app.audit_logs
    USING (
        organization_id IS NOT NULL
        AND organization_id::text = NULLIF(current_setting('app.current_org_id', true), '')
    );

GRANT SELECT, INSERT ON app.audit_logs TO pgquerynarrative_app;

-- Atomic webhook retry claims (lease fields; status stays failed until attempt finishes).
ALTER TABLE app.webhook_deliveries
    ADD COLUMN IF NOT EXISTS claimed_by TEXT,
    ADD COLUMN IF NOT EXISTS claimed_until TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_claim
    ON app.webhook_deliveries(status, claimed_until)
    WHERE status = 'failed';
