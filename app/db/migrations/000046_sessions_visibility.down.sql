DROP FUNCTION IF EXISTS app.list_organization_connection_secrets();
DROP FUNCTION IF EXISTS app.revoke_browser_session(uuid);
DROP FUNCTION IF EXISTS app.update_browser_session(uuid, uuid, text, timestamptz, text);
DROP FUNCTION IF EXISTS app.insert_browser_session(uuid, text, uuid, text, timestamptz, text);
DROP FUNCTION IF EXISTS app.touch_browser_session(uuid);
DROP FUNCTION IF EXISTS app.get_browser_session(uuid);
DROP FUNCTION IF EXISTS app.revoke_browser_sessions_for_user(text, uuid);

DROP POLICY IF EXISTS browser_sessions_org ON app.browser_sessions;
DROP TABLE IF EXISTS app.browser_sessions;

ALTER TABLE app.dashboards DROP CONSTRAINT IF EXISTS dashboards_visibility_check;
ALTER TABLE app.schedules DROP CONSTRAINT IF EXISTS schedules_visibility_check;
ALTER TABLE app.reports DROP CONSTRAINT IF EXISTS reports_visibility_check;
ALTER TABLE app.saved_queries DROP CONSTRAINT IF EXISTS saved_queries_visibility_check;

ALTER TABLE app.dashboards DROP COLUMN IF EXISTS visibility;
ALTER TABLE app.dashboards DROP COLUMN IF EXISTS created_by;
ALTER TABLE app.schedules DROP COLUMN IF EXISTS visibility;
ALTER TABLE app.reports DROP COLUMN IF EXISTS visibility;
ALTER TABLE app.saved_queries DROP COLUMN IF EXISTS visibility;
