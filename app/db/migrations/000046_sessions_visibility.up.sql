-- P1: server-side revocable browser sessions + resource visibility/ownership.

CREATE TABLE IF NOT EXISTS app.browser_sessions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id TEXT NOT NULL,
    organization_id UUID NOT NULL REFERENCES app.organizations(id) ON DELETE CASCADE,
    role TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    sealed_refresh_token TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_browser_sessions_user
    ON app.browser_sessions(user_id) WHERE revoked_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_browser_sessions_org_user
    ON app.browser_sessions(organization_id, user_id) WHERE revoked_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_browser_sessions_expires
    ON app.browser_sessions(expires_at) WHERE revoked_at IS NULL;

ALTER TABLE app.browser_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.browser_sessions FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS browser_sessions_org ON app.browser_sessions;
CREATE POLICY browser_sessions_org ON app.browser_sessions
    USING (organization_id::text = NULLIF(current_setting('app.current_org_id', true), ''));

GRANT SELECT, INSERT, UPDATE, DELETE ON app.browser_sessions TO pgquerynarrative_app;

-- Startup/admin helpers that must see sessions across orgs (revoke-all-for-user).
CREATE OR REPLACE FUNCTION app.revoke_browser_sessions_for_user(p_user_id text, p_organization_id uuid DEFAULT NULL)
RETURNS bigint
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = app, pg_temp
AS $$
DECLARE
  n bigint;
BEGIN
  UPDATE app.browser_sessions
  SET revoked_at = NOW()
  WHERE user_id = p_user_id
    AND revoked_at IS NULL
    AND (p_organization_id IS NULL OR organization_id = p_organization_id);
  GET DIAGNOSTICS n = ROW_COUNT;
  RETURN n;
END;
$$;

CREATE OR REPLACE FUNCTION app.get_browser_session(p_id uuid)
RETURNS TABLE (
    id uuid,
    user_id text,
    organization_id uuid,
    role text,
    expires_at timestamptz,
    revoked_at timestamptz,
    sealed_refresh_token text
)
LANGUAGE sql
SECURITY DEFINER
SET search_path = app, pg_temp
AS $$
  SELECT s.id, s.user_id, s.organization_id, s.role, s.expires_at, s.revoked_at, s.sealed_refresh_token
  FROM app.browser_sessions s
  WHERE s.id = p_id;
$$;

CREATE OR REPLACE FUNCTION app.touch_browser_session(p_id uuid)
RETURNS void
LANGUAGE sql
SECURITY DEFINER
SET search_path = app, pg_temp
AS $$
  UPDATE app.browser_sessions SET last_seen_at = NOW() WHERE id = p_id AND revoked_at IS NULL;
$$;

CREATE OR REPLACE FUNCTION app.insert_browser_session(
    p_id uuid,
    p_user_id text,
    p_organization_id uuid,
    p_role text,
    p_expires_at timestamptz,
    p_sealed_refresh_token text
)
RETURNS void
LANGUAGE sql
SECURITY DEFINER
SET search_path = app, pg_temp
AS $$
  INSERT INTO app.browser_sessions (id, user_id, organization_id, role, expires_at, sealed_refresh_token)
  VALUES (p_id, p_user_id, p_organization_id, p_role, p_expires_at, p_sealed_refresh_token);
$$;

CREATE OR REPLACE FUNCTION app.update_browser_session(
    p_id uuid,
    p_organization_id uuid,
    p_role text,
    p_expires_at timestamptz,
    p_sealed_refresh_token text
)
RETURNS void
LANGUAGE sql
SECURITY DEFINER
SET search_path = app, pg_temp
AS $$
  UPDATE app.browser_sessions
  SET organization_id = p_organization_id,
      role = p_role,
      expires_at = p_expires_at,
      sealed_refresh_token = p_sealed_refresh_token,
      last_seen_at = NOW()
  WHERE id = p_id AND revoked_at IS NULL;
$$;

CREATE OR REPLACE FUNCTION app.revoke_browser_session(p_id uuid)
RETURNS void
LANGUAGE sql
SECURITY DEFINER
SET search_path = app, pg_temp
AS $$
  UPDATE app.browser_sessions SET revoked_at = NOW() WHERE id = p_id AND revoked_at IS NULL;
$$;

REVOKE ALL ON FUNCTION app.revoke_browser_sessions_for_user(text, uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION app.get_browser_session(uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION app.touch_browser_session(uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION app.insert_browser_session(uuid, text, uuid, text, timestamptz, text) FROM PUBLIC;
REVOKE ALL ON FUNCTION app.update_browser_session(uuid, uuid, text, timestamptz, text) FROM PUBLIC;
REVOKE ALL ON FUNCTION app.revoke_browser_session(uuid) FROM PUBLIC;

GRANT EXECUTE ON FUNCTION app.revoke_browser_sessions_for_user(text, uuid) TO pgquerynarrative_app;
GRANT EXECUTE ON FUNCTION app.get_browser_session(uuid) TO pgquerynarrative_app;
GRANT EXECUTE ON FUNCTION app.touch_browser_session(uuid) TO pgquerynarrative_app;
GRANT EXECUTE ON FUNCTION app.insert_browser_session(uuid, text, uuid, text, timestamptz, text) TO pgquerynarrative_app;
GRANT EXECUTE ON FUNCTION app.update_browser_session(uuid, uuid, text, timestamptz, text) TO pgquerynarrative_app;
-- List enabled tenant secrets across orgs for startup security audits.
CREATE OR REPLACE FUNCTION app.list_organization_connection_secrets()
RETURNS TABLE (organization_id uuid, connection_id text)
LANGUAGE sql
SECURITY DEFINER
SET search_path = app, pg_temp
AS $$
  SELECT s.organization_id, s.connection_id
  FROM app.organization_connection_secrets s
  WHERE s.enabled = true;
$$;
REVOKE ALL ON FUNCTION app.list_organization_connection_secrets() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.list_organization_connection_secrets() TO pgquerynarrative_app;

ALTER TABLE app.saved_queries
    ADD COLUMN IF NOT EXISTS visibility TEXT NOT NULL DEFAULT 'organization';
ALTER TABLE app.reports
    ADD COLUMN IF NOT EXISTS visibility TEXT NOT NULL DEFAULT 'organization';
ALTER TABLE app.schedules
    ADD COLUMN IF NOT EXISTS visibility TEXT NOT NULL DEFAULT 'organization';
ALTER TABLE app.dashboards
    ADD COLUMN IF NOT EXISTS created_by TEXT;
ALTER TABLE app.dashboards
    ADD COLUMN IF NOT EXISTS visibility TEXT NOT NULL DEFAULT 'organization';

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE conname = 'saved_queries_visibility_check'
  ) THEN
    ALTER TABLE app.saved_queries
      ADD CONSTRAINT saved_queries_visibility_check
      CHECK (visibility IN ('private', 'team', 'organization'));
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE conname = 'reports_visibility_check'
  ) THEN
    ALTER TABLE app.reports
      ADD CONSTRAINT reports_visibility_check
      CHECK (visibility IN ('private', 'team', 'organization'));
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE conname = 'schedules_visibility_check'
  ) THEN
    ALTER TABLE app.schedules
      ADD CONSTRAINT schedules_visibility_check
      CHECK (visibility IN ('private', 'team', 'organization'));
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE conname = 'dashboards_visibility_check'
  ) THEN
    ALTER TABLE app.dashboards
      ADD CONSTRAINT dashboards_visibility_check
      CHECK (visibility IN ('private', 'team', 'organization'));
  END IF;
END $$;
