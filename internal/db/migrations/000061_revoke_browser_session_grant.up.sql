-- Migration 000046 granted EXECUTE on 5 of the 6 browser-session functions it created to
-- pgquerynarrative_app, but missed app.revoke_browser_session(uuid) — the single-session-by-ID
-- revoke that SessionManager.RevokeCurrent calls on every logout. Since that call's error is
-- discarded (best-effort revoke, so a DB hiccup never blocks clearing the cookie), the missing
-- grant was invisible: logout always appeared to succeed, but the server-side session row was
-- never actually revoked, so a copy of the session cookie taken before logout stayed valid until
-- its natural expiry instead of being cut off immediately.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'pgquerynarrative_app') THEN
    GRANT EXECUTE ON FUNCTION app.revoke_browser_session(uuid) TO pgquerynarrative_app;
  END IF;
END
$$;
