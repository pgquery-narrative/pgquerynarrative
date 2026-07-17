DROP INDEX IF EXISTS app.idx_webhook_deliveries_claim;
ALTER TABLE app.webhook_deliveries DROP COLUMN IF EXISTS claimed_until;
ALTER TABLE app.webhook_deliveries DROP COLUMN IF EXISTS claimed_by;

DROP POLICY IF EXISTS audit_logs_org ON app.audit_logs;
ALTER TABLE app.audit_logs DISABLE ROW LEVEL SECURITY;
DROP INDEX IF EXISTS app.idx_audit_logs_org_created;
ALTER TABLE app.audit_logs DROP COLUMN IF EXISTS organization_id;

DROP POLICY IF EXISTS dashboard_widgets_org ON app.dashboard_widgets;
ALTER TABLE app.dashboard_widgets DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS ask_messages_org ON app.ask_messages;
ALTER TABLE app.ask_messages DISABLE ROW LEVEL SECURITY;

DROP FUNCTION IF EXISTS app.resolve_report_share_token(text);
DROP POLICY IF EXISTS report_share_tokens_org ON app.report_share_tokens;
ALTER TABLE app.report_share_tokens DISABLE ROW LEVEL SECURITY;
DROP INDEX IF EXISTS app.idx_report_share_tokens_org;
ALTER TABLE app.report_share_tokens DROP COLUMN IF EXISTS organization_id;
