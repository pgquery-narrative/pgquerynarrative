-- PgQueryNarrative extension: call API from SQL.
-- Optional: CREATE EXTENSION http; before this for real API calls (else functions return pending JSON).

CREATE OR REPLACE FUNCTION pgquerynarrative_set_api_url(url TEXT)
RETURNS void LANGUAGE plpgsql AS $config$
BEGIN
  PERFORM set_config('pgquerynarrative.api_url', url, false);
END;
$config$;

CREATE OR REPLACE FUNCTION pgquerynarrative_get_api_url()
RETURNS TEXT LANGUAGE plpgsql STABLE AS $config$
BEGIN
  RETURN COALESCE(current_setting('pgquerynarrative.api_url', true), 'http://localhost:8080');
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
      request_body TEXT;
      response http_response;
      result JSON;
    BEGIN
      api_url := pgquerynarrative_get_api_url();
      request_body := json_build_object('sql', query_sql, 'limit', row_limit)::text;
      SELECT * INTO response FROM http((
        'POST', api_url || '/api/v1/queries/run',
        ARRAY[http_header('Content-Type', 'application/json')],
        'application/json', request_body
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
      request_body TEXT;
      response http_response;
      result JSON;
    BEGIN
      api_url := pgquerynarrative_get_api_url();
      request_body := json_build_object('sql', query_sql)::text;
      SELECT * INTO response FROM http((
        'POST', api_url || '/api/v1/reports/generate',
        ARRAY[http_header('Content-Type', 'application/json')],
        'application/json', request_body
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
      response http_response;
      result JSON;
    BEGIN
      api_url := pgquerynarrative_get_api_url();
      SELECT * INTO response FROM http((
        'GET', api_url || '/api/v1/queries/saved?limit=' || query_limit || '&offset=' || query_offset,
        ARRAY[]::http_header[]
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

GRANT EXECUTE ON FUNCTION pgquerynarrative_set_api_url(TEXT) TO PUBLIC;
GRANT EXECUTE ON FUNCTION pgquerynarrative_get_api_url() TO PUBLIC;
GRANT EXECUTE ON FUNCTION pgquerynarrative_run_query(TEXT, INTEGER) TO PUBLIC;
GRANT EXECUTE ON FUNCTION pgquerynarrative_generate_report(TEXT) TO PUBLIC;
GRANT EXECUTE ON FUNCTION pgquerynarrative_list_saved(INTEGER, INTEGER) TO PUBLIC;
