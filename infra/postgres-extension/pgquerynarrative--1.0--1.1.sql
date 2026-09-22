-- PgQueryNarrative extension upgrade 1.0 -> 1.1. Same statements as pgquerynarrative--1.1.sql,
-- which are idempotent, so an upgraded database ends up identical to a fresh 1.1 install.
-- Behaviour change: PUBLIC loses EXECUTE. Run pgquerynarrative_grant_access(role) for each role that needs it.
-- Optional: CREATE EXTENSION http; before this for real API calls (else functions return pending JSON).
--
-- Changes from 1.0 (see pgquerynarrative--1.0--1.1.sql, which carries the same statements):
--   * No function is executable by PUBLIC. Grant access with pgquerynarrative_grant_access(role).
--   * The API URL is a stored setting that only the extension owner can change. In 1.0 any
--     role could repoint it for its own session, which let any role make the database server
--     send requests to an address of its choosing.
--   * Each session can supply its own API key with pgquerynarrative_set_api_key(); it is sent
--     as an Authorization: Bearer header.
--
-- Every statement below is idempotent so the upgrade script can reuse it unchanged.

CREATE TABLE IF NOT EXISTS pgquerynarrative_config (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
REVOKE ALL ON TABLE pgquerynarrative_config FROM PUBLIC;
SELECT pg_catalog.pg_extension_config_dump('pgquerynarrative_config', '');

-- Read the stored URL. SECURITY DEFINER so callers need no privilege on the config table;
-- it takes no input and returns one setting, and search_path is pinned.
CREATE OR REPLACE FUNCTION pgquerynarrative_get_api_url()
RETURNS TEXT LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $config$
  SELECT COALESCE(
    (SELECT value FROM @extschema@.pgquerynarrative_config WHERE key = 'api_url'),
    'http://localhost:8080');
$config$;

-- Change the stored URL. Runs with the caller's own rights, so only a role that can write
-- the config table (the extension owner) succeeds. It is not granted to anyone else.
CREATE OR REPLACE FUNCTION pgquerynarrative_set_api_url(url TEXT)
RETURNS void LANGUAGE plpgsql AS $config$
BEGIN
  IF url IS NULL OR url !~* '^https?://[^[:space:]]+$' THEN
    RAISE EXCEPTION 'pgquerynarrative: api url must start with http:// or https:// and contain no spaces';
  END IF;
  INSERT INTO @extschema@.pgquerynarrative_config (key, value)
  VALUES ('api_url', regexp_replace(url, '/+$', ''))
  ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value;
END;
$config$;

-- Session-scoped and per user: each caller supplies their own key, and it is sent only to
-- the URL the owner configured. Note that statement logging (log_statement = 'all')
-- records the literal.
CREATE OR REPLACE FUNCTION pgquerynarrative_set_api_key(api_key TEXT)
RETURNS void LANGUAGE plpgsql AS $config$
BEGIN
  PERFORM set_config('pgquerynarrative.api_key', COALESCE(api_key, ''), false);
END;
$config$;

DO $ext$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'http') THEN
    -- HTTP extension present: create real API-calling functions
    CREATE OR REPLACE FUNCTION pgquerynarrative_run_query(query_sql TEXT, row_limit INTEGER DEFAULT 100)
    RETURNS JSON LANGUAGE plpgsql AS $fn$
    DECLARE
      api_url TEXT;
      api_key TEXT;
      hdrs http_header[];
      request_body TEXT;
      response http_response;
      result JSON;
    BEGIN
      api_url := pgquerynarrative_get_api_url();
      api_key := NULLIF(current_setting('pgquerynarrative.api_key', true), '');
      hdrs := ARRAY[]::http_header[];
      IF api_key IS NOT NULL THEN
        hdrs := hdrs || http_header('Authorization', 'Bearer ' || api_key);
      END IF;
      request_body := json_build_object('sql', query_sql, 'limit', row_limit)::text;
      response := http((
        'POST', api_url || '/api/v1/queries/run',
        hdrs, 'application/json', request_body
      )::http_request);
      IF response.status = 200 THEN
        result := response.content::json;
      ELSE
        RAISE EXCEPTION 'PgQueryNarrative API error: % - %', response.status, response.content;
      END IF;
      RETURN result;
    END;
    $fn$;

    CREATE OR REPLACE FUNCTION pgquerynarrative_generate_report(query_sql TEXT)
    RETURNS JSON LANGUAGE plpgsql AS $fn$
    DECLARE
      api_url TEXT;
      api_key TEXT;
      hdrs http_header[];
      request_body TEXT;
      response http_response;
      result JSON;
    BEGIN
      api_url := pgquerynarrative_get_api_url();
      api_key := NULLIF(current_setting('pgquerynarrative.api_key', true), '');
      hdrs := ARRAY[]::http_header[];
      IF api_key IS NOT NULL THEN
        hdrs := hdrs || http_header('Authorization', 'Bearer ' || api_key);
      END IF;
      request_body := json_build_object('sql', query_sql)::text;
      response := http((
        'POST', api_url || '/api/v1/reports/generate',
        hdrs, 'application/json', request_body
      )::http_request);
      IF response.status = 200 THEN
        result := response.content::json;
      ELSE
        RAISE EXCEPTION 'PgQueryNarrative API error: % - %', response.status, response.content;
      END IF;
      RETURN result;
    END;
    $fn$;

    CREATE OR REPLACE FUNCTION pgquerynarrative_list_saved(query_limit INTEGER DEFAULT 50, query_offset INTEGER DEFAULT 0)
    RETURNS JSON LANGUAGE plpgsql AS $fn$
    DECLARE
      api_url TEXT;
      api_key TEXT;
      hdrs http_header[];
      response http_response;
      result JSON;
    BEGIN
      api_url := pgquerynarrative_get_api_url();
      api_key := NULLIF(current_setting('pgquerynarrative.api_key', true), '');
      hdrs := ARRAY[]::http_header[];
      IF api_key IS NOT NULL THEN
        hdrs := hdrs || http_header('Authorization', 'Bearer ' || api_key);
      END IF;
      response := http((
        'GET', api_url || '/api/v1/queries/saved?limit=' || query_limit || '&offset=' || query_offset,
        hdrs, NULL, NULL
      )::http_request);
      IF response.status = 200 THEN
        result := response.content::json;
      ELSE
        RAISE EXCEPTION 'PgQueryNarrative API error: % - %', response.status, response.content;
      END IF;
      RETURN result;
    END;
    $fn$;
  ELSE
    -- No http: stub functions return pending JSON
    CREATE OR REPLACE FUNCTION pgquerynarrative_run_query(query_sql TEXT, row_limit INTEGER DEFAULT 100)
    RETURNS JSON LANGUAGE plpgsql AS $fn$
    BEGIN
      RETURN json_build_object(
        'status', 'pending',
        'message', 'Install extension http for API calls: CREATE EXTENSION http;',
        'api_url', pgquerynarrative_get_api_url(), 'query', query_sql, 'limit', row_limit
      );
    END;
    $fn$;

    CREATE OR REPLACE FUNCTION pgquerynarrative_generate_report(query_sql TEXT)
    RETURNS JSON LANGUAGE plpgsql AS $fn$
    BEGIN
      RETURN json_build_object(
        'status', 'pending',
        'message', 'Install extension http for API calls: CREATE EXTENSION http;',
        'api_url', pgquerynarrative_get_api_url(), 'query', query_sql
      );
    END;
    $fn$;

    CREATE OR REPLACE FUNCTION pgquerynarrative_list_saved(query_limit INTEGER DEFAULT 50, query_offset INTEGER DEFAULT 0)
    RETURNS JSON LANGUAGE plpgsql AS $fn$
    BEGIN
      RETURN json_build_object(
        'status', 'pending',
        'message', 'Install extension http for API calls: CREATE EXTENSION http;',
        'api_url', pgquerynarrative_get_api_url(), 'limit', query_limit, 'offset', query_offset
      );
    END;
    $fn$;
  END IF;
END;
$ext$;

-- Grant or withdraw the five callable functions for one role. Run by the extension owner
-- (or a superuser). pgquerynarrative_set_api_url is never included: it stays owner-only.
CREATE OR REPLACE FUNCTION pgquerynarrative_grant_access(to_role NAME)
RETURNS void LANGUAGE plpgsql AS $config$
DECLARE
  sig TEXT;
BEGIN
  FOREACH sig IN ARRAY ARRAY[
    'pgquerynarrative_get_api_url()',
    'pgquerynarrative_set_api_key(text)',
    'pgquerynarrative_run_query(text, integer)',
    'pgquerynarrative_generate_report(text)',
    'pgquerynarrative_list_saved(integer, integer)'
  ] LOOP
    EXECUTE format('GRANT EXECUTE ON FUNCTION %s TO %I', to_regprocedure('@extschema@.' || sig), to_role);
  END LOOP;
END;
$config$;

CREATE OR REPLACE FUNCTION pgquerynarrative_revoke_access(from_role NAME)
RETURNS void LANGUAGE plpgsql AS $config$
DECLARE
  sig TEXT;
BEGIN
  FOREACH sig IN ARRAY ARRAY[
    'pgquerynarrative_get_api_url()',
    'pgquerynarrative_set_api_key(text)',
    'pgquerynarrative_run_query(text, integer)',
    'pgquerynarrative_generate_report(text)',
    'pgquerynarrative_list_saved(integer, integer)'
  ] LOOP
    EXECUTE format('REVOKE EXECUTE ON FUNCTION %s FROM %I', to_regprocedure('@extschema@.' || sig), from_role);
  END LOOP;
END;
$config$;

-- Nothing is executable by PUBLIC. PostgreSQL grants EXECUTE to PUBLIC on every new
-- function, so it is withdrawn explicitly.
REVOKE ALL ON FUNCTION pgquerynarrative_get_api_url() FROM PUBLIC;
REVOKE ALL ON FUNCTION pgquerynarrative_set_api_url(TEXT) FROM PUBLIC;
REVOKE ALL ON FUNCTION pgquerynarrative_set_api_key(TEXT) FROM PUBLIC;
REVOKE ALL ON FUNCTION pgquerynarrative_run_query(TEXT, INTEGER) FROM PUBLIC;
REVOKE ALL ON FUNCTION pgquerynarrative_generate_report(TEXT) FROM PUBLIC;
REVOKE ALL ON FUNCTION pgquerynarrative_list_saved(INTEGER, INTEGER) FROM PUBLIC;
REVOKE ALL ON FUNCTION pgquerynarrative_grant_access(NAME) FROM PUBLIC;
REVOKE ALL ON FUNCTION pgquerynarrative_revoke_access(NAME) FROM PUBLIC;
