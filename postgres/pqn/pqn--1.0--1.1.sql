\echo Use "ALTER EXTENSION pqn UPDATE TO '1.1'" to load this file. \quit

-- PgQueryNarrative "pqn" 1.1: the whole pitch, inside the database.
--
--   "We do not ask you for the rewrite. We propose it from the plan, then prove it."
--
--   propose  pqn_api.investigate(sql)    plan, findings, index advice, all recorded
--            pqn rewrite engine (CLI)    sargable rewrites, OR->UNION ALL, IN->EXISTS
--   prove    pqn_api.prove(id, a, b)     runs both, compares the rows by fingerprint, times both,
--                                        records a verdict. Never "faster" for a wrong answer.
--
-- Also: table exposure scopes, unqualified names in run(), plans that compose with record_*.
--
-- The installer must still be a member of the four owner roles while this runs.

GRANT CREATE ON SCHEMA pqn_api TO pqn_owner, pqn_reader, pqn_stats, pqn_ledger;

-- ---------------------------------------------------------------------------------------
-- Ledger migration 2 and the exposure registry are created by init(); replace init() first.
-- ---------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION pqn_api.init() RETURNS text
LANGUAGE plpgsql AS $f$
DECLARE
  saved name := current_user;
  have int;
  reg int;
BEGIN
  IF NOT (pg_has_role(current_user, 'pqn_owner', 'MEMBER') AND pg_has_role(current_user, 'pqn_ledger', 'MEMBER')) THEN
    RAISE EXCEPTION 'pqn: run init() as the installer, who must be a member of pqn_owner and pqn_ledger';
  END IF;

  IF NOT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = 'pqn') THEN
    CREATE SCHEMA pqn AUTHORIZATION pqn_owner;
  END IF;
  GRANT USAGE ON SCHEMA pqn TO pqn_reader;

  IF NOT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = 'pqn_ledger') THEN
    CREATE SCHEMA pqn_ledger AUTHORIZATION pqn_ledger;
  END IF;

  EXECUTE 'SET LOCAL ROLE pqn_ledger';
  CREATE TABLE IF NOT EXISTS pqn_ledger.schema_migrations (
    version    integer PRIMARY KEY,
    applied_at timestamptz NOT NULL DEFAULT now()
  );
  SELECT COALESCE(max(version), 0) INTO have FROM pqn_ledger.schema_migrations;

  IF have < 1 THEN
    CREATE TABLE pqn_ledger.investigations (
      id         bigserial PRIMARY KEY,
      who        text NOT NULL DEFAULT session_user,
      title      text,
      queryid    bigint,
      sql        text NOT NULL,
      created_at timestamptz NOT NULL DEFAULT now()
    );
    CREATE INDEX investigations_who_idx ON pqn_ledger.investigations (who, id DESC);
    CREATE TABLE pqn_ledger.evidence (
      id               bigserial PRIMARY KEY,
      investigation_id bigint NOT NULL REFERENCES pqn_ledger.investigations (id),
      kind             text NOT NULL,
      payload          jsonb NOT NULL,
      who              text NOT NULL DEFAULT session_user,
      created_at       timestamptz NOT NULL DEFAULT now()
    );
    CREATE INDEX evidence_investigation_idx ON pqn_ledger.evidence (investigation_id, id);
    INSERT INTO pqn_ledger.schema_migrations (version) VALUES (1);
    have := 1;
  END IF;
  -- Migration 2 (1.1) is the exposure registry in schema pqn. It belongs to pqn_owner, not the
  -- ledger, so it is created below, but its version is recorded here.
  EXECUTE format('SET LOCAL ROLE %I', saved);

  EXECUTE 'SET LOCAL ROLE pqn_owner';
  CREATE TABLE IF NOT EXISTS pqn.exposed (
    schema_name name NOT NULL,
    table_name  name NOT NULL,
    view_name   name PRIMARY KEY,
    scope       text NOT NULL CHECK (scope IN ('view', 'full')),
    columns     text[],
    exposed_at  timestamptz NOT NULL DEFAULT now(),
    exposed_by  text NOT NULL DEFAULT session_user
  );
  -- The timeouts each person was enrolled with. They live here, in the administrators' hands,
  -- because the copies on the login role are defaults the person can lift for their own session
  -- or remove (ALTER ROLE ... RESET). enforce_limits() reads these.
  CREATE TABLE IF NOT EXISTS pqn.limits (
    login                          name PRIMARY KEY,
    statement_timeout_ms           bigint NOT NULL CHECK (statement_timeout_ms > 0),
    lock_timeout_ms                bigint NOT NULL CHECK (lock_timeout_ms > 0),
    idle_in_transaction_timeout_ms bigint NOT NULL CHECK (idle_in_transaction_timeout_ms > 0),
    set_at               timestamptz NOT NULL DEFAULT now(),
    set_by               text NOT NULL DEFAULT session_user
  );
  GRANT USAGE ON SCHEMA pqn TO pqn_admin;
  GRANT SELECT ON pqn.limits TO pqn_admin;
  EXECUTE format('SET LOCAL ROLE %I', saved);

  SELECT count(*) INTO reg FROM pqn.exposed;
  IF reg = 0 THEN
    -- Views made under 1.0 have no registry rows. Backfill them from the catalogs.
    EXECUTE 'SET LOCAL ROLE pqn_owner';
    INSERT INTO pqn.exposed (schema_name, table_name, view_name, scope, columns)
    SELECT DISTINCT tn.nspname, t.relname, v.relname, 'view', NULL::text[]
      FROM pg_class v
      JOIN pg_namespace vn ON vn.oid = v.relnamespace AND vn.nspname = 'pqn'
      JOIN pg_rewrite rw ON rw.ev_class = v.oid
      JOIN pg_depend d ON d.objid = rw.oid AND d.classid = 'pg_rewrite'::regclass
                      AND d.refclassid = 'pg_class'::regclass AND d.refobjid <> v.oid
      JOIN pg_class t ON t.oid = d.refobjid AND t.relkind IN ('r', 'p', 'v', 'm', 'f')
      JOIN pg_namespace tn ON tn.oid = t.relnamespace
     WHERE v.relkind = 'v' AND tn.nspname <> 'pqn'
    ON CONFLICT (view_name) DO NOTHING;
    EXECUTE format('SET LOCAL ROLE %I', saved);
  END IF;

  EXECUTE 'SET LOCAL ROLE pqn_ledger';
  INSERT INTO pqn_ledger.schema_migrations (version) VALUES (2) ON CONFLICT DO NOTHING;
  EXECUTE format('SET LOCAL ROLE %I', saved);
  RETURN format('ledger at version %s, exposure registry ready', GREATEST(have, 2));
END
$f$;

-- ---------------------------------------------------------------------------------------
-- Replace the two functions whose behaviour changes, as their owners so ownership is kept.
-- ---------------------------------------------------------------------------------------

-- run(): plain table names now resolve to the curated views, so analysts write
-- "FROM orders", not "FROM pqn.orders". pg_catalog stays first, so a view can never shadow a
-- built-in function, and the caller cannot change the path (a definer function pins it).
SET LOCAL ROLE pqn_reader;
CREATE OR REPLACE FUNCTION pqn_api.run(l_query text, row_limit integer DEFAULT 100) RETURNS jsonb
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pqn, pg_temp AS $f$
DECLARE
  c refcursor;
  r record;
  acc jsonb[] := '{}';
  cols jsonb := '[]'::jsonb;
  v_names text[];
  v_base text;
  v_k integer;
  v_row jsonb;
  v_renamed boolean := false;
  v_bytes bigint := 0;
  n integer := 0;
  lim integer := least(greatest(coalesce(row_limit, 100), 1), 10000);
BEGIN
  IF l_query IS NULL OR l_query ~ '^(\s|;|--[^\n]*|/\*([^*]|\*+[^*/])*\*+/)*$' THEN
    RAISE EXCEPTION 'pqn: give a statement to run';
  END IF;
  SET TRANSACTION READ ONLY;
  OPEN c FOR EXECUTE l_query;
  LOOP
    FETCH c INTO r;
    EXIT WHEN NOT FOUND;
    n := n + 1;
    -- Size the row before copying it. to_json and to_jsonb each make a full copy, and one 150 MB row
    -- took this backend past 1 GB. The row itself is only as large as the statement made it, which
    -- PostgreSQL allows any SELECT to do; what this function adds is copies, so it makes none of a
    -- row it is going to refuse.
    IF pg_column_size(r) > 16777216 THEN
      CLOSE c;
      RETURN jsonb_build_object('columns', cols, 'rows', to_jsonb(acc), 'truncated', true);
    END IF;
    IF n = 1 THEN
      -- jsonb does not keep key order, json does, so the column order comes from json.
      SELECT array_agg(k ORDER BY o) INTO v_names FROM json_object_keys(to_json(r)) WITH ORDINALITY AS t(k, o);
      -- A jsonb object holds each name once, so SELECT 1, 2 or max(a), max(b) would keep only the
      -- last value under a name listed twice. Make the names unique instead of losing a column.
      FOR i IN 1..COALESCE(array_length(v_names, 1), 0) LOOP
        v_base := v_names[i];
        v_k := 1;
        WHILE v_names[i] = ANY (v_names[1:i - 1]) LOOP
          v_k := v_k + 1;
          v_names[i] := v_base || '_' || v_k;
          v_renamed := true;
        END LOOP;
      END LOOP;
      cols := COALESCE(to_jsonb(v_names), '[]'::jsonb);
    END IF;
    IF v_renamed THEN
      SELECT jsonb_object_agg(nm.name, e.value::jsonb) INTO v_row
        FROM json_each(to_json(r)) WITH ORDINALITY AS e(key, value, ord)
        JOIN unnest(v_names) WITH ORDINALITY AS nm(name, ord) USING (ord);
    ELSE
      v_row := to_jsonb(r);
    END IF;
    -- row_limit bounds rows, not bytes: 40 rows of 90 MB would be held in this backend's memory and
    -- the operating system would kill it, restarting every session. The budget bounds the answer.
    v_bytes := v_bytes + pg_column_size(v_row);
    IF n > lim OR v_bytes > 16777216 THEN
      CLOSE c;
      RETURN jsonb_build_object('columns', cols, 'rows', to_jsonb(acc), 'truncated', true);
    END IF;
    acc := acc || v_row;
  END LOOP;
  CLOSE c;
  IF n = 0 THEN
    -- No row carries the column names, so ask for one row of NULLs shaped like the answer. It is a
    -- convenience: a statement that cannot be wrapped (SHOW, EXPLAIN) simply has no names.
    BEGIN
      OPEN c FOR EXECUTE 'SELECT pqn_t.* FROM (SELECT 1) AS pqn_d LEFT JOIN (' || l_query || E'\n) AS pqn_t ON false';
      FETCH c INTO r;
      IF FOUND THEN
        SELECT COALESCE(jsonb_agg(k), '[]'::jsonb) INTO cols FROM json_object_keys(to_json(r)) AS k;
      END IF;
      CLOSE c;
    EXCEPTION WHEN OTHERS THEN
      NULL;
    END;
  END IF;
  RETURN jsonb_build_object('columns', cols, 'rows', to_jsonb(acc), 'truncated', false);
END
$f$;
RESET ROLE;

-- top(): only statements pg_stat_statements has normalized. It keeps utility statements
-- (ALTER ROLE ... PASSWORD 'x', CREATE USER MAPPING, COPY, PREPARE) exactly as typed, literals and
-- all, and this function runs as pg_monitor for every analyst. A statement that starts with a
-- query keyword has its constants replaced by $n; everything else is left out.
SET LOCAL ROLE pqn_stats;
CREATE OR REPLACE FUNCTION pqn_api.top(n integer DEFAULT 20)
RETURNS TABLE (queryid bigint, query text, calls bigint, total_exec_time double precision,
               mean_exec_time double precision, "rows" bigint)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $f$
#variable_conflict use_column
DECLARE
  ns name;
BEGIN
  SELECT x.nspname INTO ns
  FROM pg_extension e JOIN pg_namespace x ON x.oid = e.extnamespace
  WHERE e.extname = 'pg_stat_statements';
  IF ns IS NULL THEN
    RAISE EXCEPTION 'pqn: pg_stat_statements is not installed in this database'
      USING HINT = 'CREATE EXTENSION pg_stat_statements; it also needs shared_preload_libraries.';
  END IF;
  RETURN QUERY EXECUTE format(
    'SELECT s.queryid, s.query, s.calls, s.total_exec_time, s.mean_exec_time, s.rows
       FROM %I.pg_stat_statements s
      WHERE s.dbid = (SELECT d.oid FROM pg_database d WHERE d.datname = current_database())
        AND regexp_replace(s.query, $2, '''') ~* $3
      ORDER BY s.total_exec_time DESC
      LIMIT $1', ns)
    USING least(greatest(coalesce(n, 20), 1), 500),
          '^(\s+|/\*([^*]|\*+[^*/])*\*+/|--[^\n]*\n)+',
          '^(select|with|insert|update|delete|merge|values|table)\M';
END
$f$;
RESET ROLE;

-- ---------------------------------------------------------------------------------------
-- Planning and measuring run as pqn_owner, which holds SELECT on what the DBA exposed.
-- ---------------------------------------------------------------------------------------
SET LOCAL ROLE pqn_owner;

-- The schemas of exposed tables, in a search path. The statements a DBA wants investigated come
-- from pg_stat_statements as their application typed them, with unqualified table names, so
-- planning and measuring must resolve names the way the application does. The list comes from
-- the registry the DBA fills through expose(), never from the caller.
CREATE FUNCTION pqn_api.exposed_path() RETURNS text
LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $f$
DECLARE
  p text;
BEGIN
  IF EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
              WHERE n.nspname = 'pqn' AND c.relname = 'exposed') THEN
    SELECT string_agg(DISTINCT quote_ident(e.schema_name), ', ' ORDER BY quote_ident(e.schema_name))
      INTO p FROM pqn.exposed e;
  END IF;
  RETURN 'pg_catalog, ' || COALESCE(p, 'public') || ', pg_temp';
END
$f$;

CREATE FUNCTION pqn_api.exposed()
RETURNS TABLE (schema_name name, table_name name, view_name name, scope text, columns text[])
LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $f$
#variable_conflict use_column
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
                  WHERE n.nspname = 'pqn' AND c.relname = 'exposed') THEN
    RETURN;
  END IF;
  RETURN QUERY SELECT e.schema_name, e.table_name, e.view_name, e.scope, e.columns
                 FROM pqn.exposed e ORDER BY e.schema_name, e.table_name, e.view_name;
END
$f$;

-- Does the statement refer to a $n parameter? One inside a string, a quoted name, a $$ string or a
-- comment is text: '$5' and $$2025-03-01$$ are not placeholders. A $tag$ string is not skipped, so
-- one holding "$1" counts as a parameter: that refuses a statement that could have run, and never runs
-- one that cannot.
CREATE FUNCTION pqn_api.has_params(l_query text) RETURNS boolean
LANGUAGE sql IMMUTABLE SET search_path = pg_catalog AS $f$
  SELECT regexp_replace(l_query,
           $re$[eE]'([^'\\]|\\.|'')*'|'([^']|'')*'|"([^"]|"")*"|--[^\n]*|/\*([^*]|\*+[^*/])*\*+/|\$\$([^$]|\$[^$])*\$\$$re$,
           ' ', 'g') ~ '\$[0-9]'
$f$;

-- Estimated plan for one statement, without running it. Owned by pqn_owner, whose SELECT on the
-- exposed tables is what lets the planner accept a statement over base tables while the caller
-- has no access to them. A cursor accepts exactly one statement. $n placeholders need
-- GENERIC_PLAN (PostgreSQL 16+). VERBOSE adds schema names, so findings can resolve tables.
--
-- EXPLAIN does not run the statement, but the planner does run the IMMUTABLE and STABLE functions
-- it folds into constants, and the caller can supply one (a function in pg_temp, which everyone may
-- create; an immutable one may call a volatile one). That code would run as pqn_owner, which owns
-- the views, the exposure registry and the limits table. So planning happens in a read-only
-- sub-transaction, the way measure_pair works: PostgreSQL cannot make it read-write again, and it
-- ends by raising a private error that rolls it back, read-only flag included, so plan(...) can
-- still be passed straight into record_evidence(...) in the caller's transaction.
CREATE OR REPLACE FUNCTION pqn_api.plan(l_query text) RETURNS jsonb
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $f$
DECLARE
  c refcursor;
  p json;
  res jsonb;
  opts text := 'VERBOSE, FORMAT JSON';
BEGIN
  IF l_query IS NULL OR l_query ~ '^(\s|;|--[^\n]*|/\*([^*]|\*+[^*/])*\*+/)*$' THEN
    RAISE EXCEPTION 'pqn: give a statement to plan';
  END IF;
  PERFORM set_config('search_path', pqn_api.exposed_path(), true);
  IF pqn_api.has_params(l_query) THEN
    opts := 'GENERIC_PLAN, VERBOSE, FORMAT JSON';
  END IF;
  BEGIN
    PERFORM set_config('transaction_read_only', 'on', false);
    OPEN c FOR EXECUTE format('EXPLAIN (%s) %s', opts, l_query);
    FETCH c INTO p;
    CLOSE c;
    res := p::jsonb;
    RAISE EXCEPTION 'planned' USING ERRCODE = 'PQN01';
  EXCEPTION
    WHEN SQLSTATE 'PQN01' THEN
      NULL;
    WHEN insufficient_privilege THEN
      -- pqn_owner reads what the DBA exposed and holds no write privilege, and PostgreSQL checks it
      -- even to plan a write. Say so, instead of "permission denied for table orders".
      IF regexp_replace(l_query, '^(\s+|/\*([^*]|\*+[^*/])*\*+/|--[^\n]*\n)+', '') ~* '^(insert|update|delete|merge)\M' THEN
        RAISE EXCEPTION 'pqn: a statement that writes cannot be planned: pqn only reads what you expose'
          USING HINT = 'Plan the SELECT that finds the rows: the WHERE clause of the UPDATE or DELETE, or the SELECT of an INSERT.',
                DETAIL = SQLERRM;
      END IF;
      RAISE;
  END;
  -- A function inside the statement can raise the same SQLSTATE. That is not a finished plan.
  IF res IS NULL THEN
    RAISE EXCEPTION 'pqn: the plan did not complete (a statement raised an error the tool uses internally)';
  END IF;
  RETURN res;
END
$f$;

-- Record the timeouts a login was enrolled with. The enroll script calls this; only administrators
-- can execute it. A person cannot reach the table, so lifting their own session timeouts or
-- resetting their role settings does not change what enforce_limits() applies. lock_ms and idle_ms
-- default to the values enroll_sql sets on the role (2s, 10s) so existing single-argument callers
-- keep recording a statement timeout without having to name the other two.
CREATE FUNCTION pqn_api.record_limit(l_login name, l_ms bigint, l_lock_ms bigint DEFAULT 2000, l_idle_ms bigint DEFAULT 10000) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $f$
BEGIN
  IF to_regclass('pqn.limits') IS NULL THEN
    RAISE EXCEPTION 'pqn: run pqn_api.init() first';
  END IF;
  IF l_ms IS NULL OR l_ms < 1 OR l_lock_ms IS NULL OR l_lock_ms < 1 OR l_idle_ms IS NULL OR l_idle_ms < 1 THEN
    RAISE EXCEPTION 'pqn: every limit must be at least 1 millisecond';
  END IF;
  IF l_login::text LIKE 'pqn\_%' OR NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = l_login AND rolcanlogin) THEN
    RAISE EXCEPTION 'pqn: % is not a role that can log in', l_login;
  END IF;
  INSERT INTO pqn.limits (login, statement_timeout_ms, lock_timeout_ms, idle_in_transaction_timeout_ms)
  VALUES (l_login, l_ms, l_lock_ms, l_idle_ms)
  ON CONFLICT (login) DO UPDATE SET statement_timeout_ms = EXCLUDED.statement_timeout_ms,
    lock_timeout_ms = EXCLUDED.lock_timeout_ms, idle_in_transaction_timeout_ms = EXCLUDED.idle_in_transaction_timeout_ms,
    set_at = now(), set_by = session_user;
END
$f$;

-- Server-side time of one statement, planning plus execution. EXPLAIN ANALYZE runs the plan to
-- the end, so it uses parallel workers exactly as the application's own statement does. A
-- cursor fetch cannot: PostgreSQL never launches workers for a row-limited fetch, so timing
-- through one overstates how slow a parallel statement is. Only measure_pair calls this, inside the
-- read-only sub-transaction described there.
CREATE FUNCTION pqn_api.explain_ms(l_query text, l_path text) RETURNS numeric
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $f$
DECLARE
  j json;
BEGIN
  PERFORM set_config('search_path', l_path, true);
  EXECUTE 'EXPLAIN (ANALYZE, TIMING OFF, SUMMARY ON, FORMAT JSON) ' || l_query INTO j;
  RETURN (j->0->>'Planning Time')::numeric + (j->0->>'Execution Time')::numeric;
END
$f$;

-- Run two statements, fingerprint their rows, and time both. The fingerprint is the row count, and
-- the sum and the xor of two independent 64-bit hashes (two seeds, 128 bits) of every row's text, so
-- it is independent of row order and reveals no row values. It is a fingerprint, not a proof: it
-- cannot tell rows that print the same, and a collision is astronomically unlikely, not impossible. Both fingerprints are taken inside one caller statement, so they share
-- one snapshot. The times come from explain_ms, which runs each statement the way the application
-- does. The order alternates each round and the fastest round counts, so neither side benefits
-- from a warm cache. Statements with $n placeholders cannot run.
--
-- The statements run as pqn_owner, so a volatile function inside one could write. They therefore run
-- in a read-only sub-transaction. PostgreSQL cannot make a transaction read-write again, and the
-- caller (prove) has to write the proof afterwards, so the sub-transaction ends by raising a private
-- error that rolls it back, read-only flag included. Nothing done inside it is kept.
CREATE FUNCTION pqn_api.measure_pair(l_a text, l_b text, repeats integer DEFAULT 2) RETURNS jsonb
LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $f$
DECLARE
  c refcursor;
  stmts text[] := ARRAY[l_a, l_b];
  reps integer := least(greatest(coalesce(repeats, 2), 1), 5);
  s text;
  i integer;
  k integer;
  side integer;
  v_path text := pqn_api.exposed_path();
  ms numeric;
  cnt bigint[] := ARRAY[NULL, NULL]::bigint[];
  sm numeric[] := ARRAY[NULL, NULL]::numeric[];
  xr bigint[] := ARRAY[NULL, NULL]::bigint[];
  sm2 numeric[] := ARRAY[NULL, NULL]::numeric[];
  xr2 bigint[] := ARRAY[NULL, NULL]::bigint[];
  best numeric[] := ARRAY[NULL, NULL]::numeric[];
  v_cnt bigint;
  v_sum numeric;
  v_xor bigint;
  v_sum2 numeric;
  v_xor2 bigint;
  same boolean;
  res jsonb;
BEGIN
  IF l_a IS NULL OR l_b IS NULL
     OR l_a ~ '^(\s|;|--[^\n]*|/\*([^*]|\*+[^*/])*\*+/)*$' OR l_b ~ '^(\s|;|--[^\n]*|/\*([^*]|\*+[^*/])*\*+/)*$' THEN
    RAISE EXCEPTION 'pqn: both statements are required';
  END IF;
  IF pqn_api.has_params(l_a) OR pqn_api.has_params(l_b) THEN
    RAISE EXCEPTION 'pqn: statements with $n placeholders cannot be executed. Substitute values first.';
  END IF;
  PERFORM set_config('search_path', v_path, true);
  BEGIN
    -- (a STABLE function may not run SET, so this goes through set_config)
    PERFORM set_config('transaction_read_only', 'on', false);
    -- Each statement must stand alone as one query. This is what stops a text that closes the
    -- wrapper below and continues.
    FOREACH s IN ARRAY stmts LOOP
      OPEN c FOR EXECUTE s;
      CLOSE c;
    END LOOP;

    FOR side IN 1..2 LOOP
      OPEN c FOR EXECUTE format(
        'SELECT count(*)::bigint, COALESCE(sum(h), 0)::numeric, COALESCE(bit_xor(h), 0)::bigint, '
        'COALESCE(sum(g), 0)::numeric, COALESCE(bit_xor(g), 0)::bigint '
        'FROM (SELECT hashtextextended(t::text, 0) AS h, hashtextextended(t::text, 1) AS g FROM (%s' || E'\n' || ') t) x', stmts[side]);
      FETCH c INTO v_cnt, v_sum, v_xor, v_sum2, v_xor2;
      CLOSE c;
      cnt[side] := v_cnt; sm[side] := v_sum; xr[side] := v_xor; sm2[side] := v_sum2; xr2[side] := v_xor2;
    END LOOP;

    FOR i IN 1..reps LOOP
      FOR k IN 1..2 LOOP
        side := CASE WHEN i % 2 = 1 THEN k ELSE 3 - k END;
        ms := round(pqn_api.explain_ms(stmts[side], v_path), 3);
        IF best[side] IS NULL OR ms < best[side] THEN best[side] := ms; END IF;
      END LOOP;
    END LOOP;

    same := cnt[1] = cnt[2] AND sm[1] = sm[2] AND xr[1] = xr[2] AND sm2[1] = sm2[2] AND xr2[1] = xr2[2];
    res := jsonb_build_object(
      'equal', same,
      'before', jsonb_build_object('rows', cnt[1], 'sum', sm[1], 'xor', xr[1], 'sum2', sm2[1], 'xor2', xr2[1], 'ms', best[1]),
      'after',  jsonb_build_object('rows', cnt[2], 'sum', sm[2], 'xor', xr[2], 'sum2', sm2[2], 'xor2', xr2[2], 'ms', best[2]),
      'speedup', CASE WHEN best[2] > 0 THEN round(best[1] / best[2], 2) END,
      'rounds', reps);
    RAISE EXCEPTION 'measured' USING ERRCODE = 'PQN01';
  EXCEPTION WHEN SQLSTATE 'PQN01' THEN
    NULL;
  END;
  -- The handler above catches its own private error. A function inside the analyst's statement can raise
  -- the same SQLSTATE, and that would look like a finished measurement. It is not one until res is set,
  -- and res is set only on the line before our own raise.
  IF res IS NULL THEN
    RAISE EXCEPTION 'pqn: the measurement did not complete (a statement raised an error the tool uses internally)';
  END IF;
  RETURN res;
END
$f$;
RESET ROLE;

-- ---------------------------------------------------------------------------------------
-- Limits. A statement timeout is a session setting: PostgreSQL lets a person raise or remove
-- their own (SET, or ALTER ROLE on their own login), and a function cannot re-arm the timer of the
-- statement that is already running, so a limit cannot be forced from inside the session. What an
-- administrator can do is act from outside it: enforce_limits() cancels any enrolled person's
-- statement that has run longer than the timeout they were enrolled with, whatever that person set.
-- Run it every few seconds from a scheduler (pg_cron, or cron with psql).
-- ---------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION pqn_api.enroll_sql(login name, grp text DEFAULT 'analyst', stmt_timeout text DEFAULT '15s') RETURNS text
LANGUAGE plpgsql AS $f$
DECLARE
  is_super boolean;
  script text;
BEGIN
  IF grp NOT IN ('viewer', 'analyst', 'admin') THEN
    RAISE EXCEPTION 'pqn: group must be viewer, analyst or admin';
  END IF;
  -- The unit is required: PostgreSQL reads a bare number as milliseconds and interval as seconds.
  IF stmt_timeout !~ '^[1-9][0-9]*(ms|s|min)$' THEN
    RAISE EXCEPTION 'pqn: statement timeout must look like 15s, 500ms or 2min (the unit is required)';
  END IF;
  IF login::text LIKE 'pqn\_%' THEN
    RAISE EXCEPTION 'pqn: % is a pqn role and cannot be enrolled', login;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = login AND rolcanlogin) THEN
    RAISE EXCEPTION 'pqn: % is not a role that can log in', login;
  END IF;
  script := array_to_string(ARRAY[
    format('GRANT %I TO %I;', 'pqn_' || grp, login),
    format('ALTER ROLE %I SET statement_timeout = %L;', login, stmt_timeout),
    format('ALTER ROLE %I SET lock_timeout = %L;', login, '2s'),
    format('ALTER ROLE %I SET idle_in_transaction_session_timeout = %L;', login, '10s'),
    format('SELECT pqn_api.record_limit(%L, %s, %s, %s);', login::text,
      (extract(epoch FROM stmt_timeout::interval) * 1000)::bigint, 2000, 10000)], E'\n');
  SELECT rolsuper INTO is_super FROM pg_roles WHERE rolname = current_user;
  IF is_super THEN
    script := script || E'\n' || format('ALTER ROLE %I SET temp_file_limit = %L;', login, '1GB');
  ELSE
    script := script || E'\n' || format('-- superuser only: ALTER ROLE %I SET temp_file_limit = %L;', login, '1GB');
  END IF;
  RETURN script;
END
$f$;

-- Cancel every enrolled person's session that has outlived one of their three limits: a running
-- statement past statement_timeout, a lock wait past lock_timeout, or an idle-in-transaction
-- session past idle_in_transaction_session_timeout. All three are session settings a person can
-- lift for themselves (SET, or ALTER ROLE ... RESET on their own login), so all three are
-- re-enforced from outside the session the same way. A running statement or a lock wait is an
-- active query: pg_cancel_backend interrupts it, matching what PostgreSQL's own statement_timeout
-- and lock_timeout do. An idle-in-transaction session has no query to interrupt, so it is ended
-- with pg_terminate_backend, matching what PostgreSQL's own idle_in_transaction_session_timeout
-- does. Runs with the caller's rights, which must let it see every session (pg_read_all_stats) and
-- signal it (pg_signal_backend), or be a superuser. Returns one row per session it acted on.
CREATE FUNCTION pqn_api.enforce_limits()
RETURNS TABLE (pid integer, login name, running interval, limit_ms bigint, cancelled boolean)
LANGUAGE plpgsql AS $f$
DECLARE
  r record;
BEGIN
  IF NOT (SELECT rolsuper FROM pg_roles WHERE rolname = current_user)
     AND NOT (pg_has_role(current_user, 'pg_read_all_stats', 'USAGE') AND pg_has_role(current_user, 'pg_signal_backend', 'USAGE')) THEN
    RAISE EXCEPTION 'pqn: enforce_limits must run as a superuser, or a role that is a member of pg_read_all_stats and pg_signal_backend'
      USING HINT = 'GRANT pg_read_all_stats, pg_signal_backend TO the role a scheduler uses.';
  END IF;
  FOR r IN
    WITH violations AS (
      -- A statement that has run longer than statement_timeout allows.
      SELECT a.pid AS v_pid, l.login AS v_login, clock_timestamp() - a.query_start AS v_running,
             l.statement_timeout_ms AS v_limit, a.query_start AS v_since, false AS v_terminate
        FROM pqn.limits l
        JOIN pg_stat_activity a ON a.usename = l.login
       WHERE a.state = 'active' AND a.backend_type = 'client backend' AND a.datname = current_database()
         AND a.pid <> pg_backend_pid()
         AND clock_timestamp() - a.query_start > make_interval(secs => l.statement_timeout_ms / 1000.0)
      UNION ALL
      -- A statement waiting on a lock longer than lock_timeout allows.
      SELECT a.pid, l.login, clock_timestamp() - a.query_start,
             l.lock_timeout_ms, a.query_start, false
        FROM pqn.limits l
        JOIN pg_stat_activity a ON a.usename = l.login
       WHERE a.wait_event_type = 'Lock' AND a.backend_type = 'client backend' AND a.datname = current_database()
         AND a.pid <> pg_backend_pid()
         AND clock_timestamp() - a.query_start > make_interval(secs => l.lock_timeout_ms / 1000.0)
      UNION ALL
      -- A session idling in a transaction longer than idle_in_transaction_session_timeout allows.
      -- There is no running query to cancel, so this one is terminated, not cancelled.
      SELECT a.pid, l.login, clock_timestamp() - a.state_change,
             l.idle_in_transaction_timeout_ms, a.state_change, true
        FROM pqn.limits l
        JOIN pg_stat_activity a ON a.usename = l.login
       WHERE a.state = 'idle in transaction' AND a.backend_type = 'client backend' AND a.datname = current_database()
         AND a.pid <> pg_backend_pid()
         AND clock_timestamp() - a.state_change > make_interval(secs => l.idle_in_transaction_timeout_ms / 1000.0)
    )
    -- The same pid can match more than one violation (e.g. active and lock-waiting at once);
    -- act on it once, for whichever violation has been running longest.
    SELECT DISTINCT ON (v_pid) v_pid, v_login, v_running, v_limit, v_terminate
      FROM violations
     ORDER BY v_pid, v_since
  LOOP
    pid := r.v_pid; login := r.v_login; running := r.v_running; limit_ms := r.v_limit;
    -- A caller without SUPERUSER may not signal a superuser's session, and PostgreSQL raises. That
    -- must not stop the pass: report the session as not cancelled and go on to the next one.
    BEGIN
      IF r.v_terminate THEN
        cancelled := pg_terminate_backend(r.v_pid);
      ELSE
        cancelled := pg_cancel_backend(r.v_pid);
      END IF;
    EXCEPTION WHEN insufficient_privilege THEN
      cancelled := false;
    END;
    RETURN NEXT;
  END LOOP;
END
$f$;

-- ---------------------------------------------------------------------------------------
-- Findings: rules over an estimated plan. Reads only catalogs, so it runs as the caller.
-- The pqn command-line tool uses the fuller Go engine. These rules are what plain psql gets.
-- ---------------------------------------------------------------------------------------
CREATE FUNCTION pqn_api.findings(l_plan jsonb) RETURNS jsonb
LANGUAGE plpgsql STABLE AS $f$
DECLARE
  node jsonb;
  res jsonb := '[]'::jsonb;
  nt text;
  rel text;
  sch text;
  filt text;
  est bigint;
  col text;
  has_idx boolean;
  fn_wrap boolean;
  -- l_plan is whatever the caller passed, so a value that is not the shape EXPLAIN prints is skipped
  -- (a "Plans" that is not an array, a cost that is not a number), never an error.
  root_cost numeric := CASE WHEN jsonb_typeof(l_plan->0->'Plan'->'Total Cost') = 'number'
                            THEN (l_plan->0->'Plan'->'Total Cost')::numeric END;
BEGIN
  IF root_cost >= 100000 THEN
    res := res || jsonb_build_array(jsonb_build_object(
      'category', 'high_cost', 'severity', 'info', 'relation', NULL,
      'message', format('Estimated total cost is %s. Investigate the largest nodes below.', round(root_cost)),
      'advice_ddl', NULL));
  END IF;

  FOR node IN
    WITH RECURSIVE n(node) AS (
      SELECT l_plan->0->'Plan'
      UNION ALL
      SELECT p FROM n, jsonb_array_elements(CASE WHEN jsonb_typeof(n.node->'Plans') = 'array'
                                                 THEN n.node->'Plans' ELSE '[]'::jsonb END) p)
    SELECT n.node FROM n
  LOOP
    nt := node->>'Node Type';
    rel := node->>'Relation Name';
    sch := node->>'Schema';
    filt := node->>'Filter';

    IF nt IN ('Seq Scan') AND rel IS NOT NULL AND filt IS NOT NULL THEN
      SELECT c.reltuples::bigint INTO est
        FROM pg_class c JOIN pg_namespace ns ON ns.oid = c.relnamespace
       WHERE c.relname = rel AND (sch IS NULL OR ns.nspname = sch) AND c.relkind IN ('r', 'p', 'm')
       ORDER BY c.reltuples DESC LIMIT 1;
      IF COALESCE(est, 0) >= 50000 THEN
        res := res || jsonb_build_array(jsonb_build_object(
          'category', 'seq_scan', 'severity', CASE WHEN est >= 1000000 THEN 'high' ELSE 'medium' END,
          'relation', COALESCE(sch || '.', '') || rel,
          'message', format('Sequential scan of about %s rows on %s, filtered by %s', est, rel, filt),
          'filter', filt, 'advice_ddl', NULL));

        fn_wrap := filt ~* '\m(date_trunc|lower|upper|to_char|extract|date_part|coalesce|substr|substring|trim|md5)\s*\('
                   OR filt ~ '\)::(date|text)\M' OR filt ~ '[a-z_]::(date|text)\M';
        IF fn_wrap THEN
          res := res || jsonb_build_array(jsonb_build_object(
            'category', 'function_on_column', 'severity', 'high',
            'relation', COALESCE(sch || '.', '') || rel,
            'message', 'The filter wraps a column in a function or cast, so no index on that column can be used. Rewrite it as a range or equality on the bare column.',
            'filter', filt, 'advice_ddl', NULL));
        ELSE
          col := substring(filt FROM '\(?(?:[a-z_][a-z0-9_]*\.)?([a-z_][a-z0-9_]*)\)?(?:::[a-z ]+)? (?:=|<=|>=|<|>) ');
          IF col IS NOT NULL THEN
            SELECT EXISTS (
              SELECT 1 FROM pg_index i
                JOIN pg_class t ON t.oid = i.indrelid
                JOIN pg_namespace tn ON tn.oid = t.relnamespace
                JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = i.indkey[0]
               WHERE t.relname = rel AND (sch IS NULL OR tn.nspname = sch)
                 AND a.attname = col AND i.indisvalid) INTO has_idx;
            IF NOT has_idx THEN
              res := res || jsonb_build_array(jsonb_build_object(
                'category', 'index_candidate', 'severity', 'medium',
                'relation', COALESCE(sch || '.', '') || rel,
                'message', format('No index leads with %I on %s. An index may replace the sequential scan. This is a heuristic: prove it before you deploy.', col, rel),
                'filter', filt,
                'advice_ddl', format('CREATE INDEX CONCURRENTLY ON %s (%I)', COALESCE(quote_ident(sch) || '.', '') || quote_ident(rel), col)));
            END IF;
          END IF;
        END IF;
      END IF;
    END IF;

    IF nt = 'Sort' AND COALESCE(CASE WHEN jsonb_typeof(node->'Plan Rows') = 'number'
                                     THEN (node->'Plan Rows')::numeric END, 0) >= 1000000 THEN
      res := res || jsonb_build_array(jsonb_build_object(
        'category', 'sort_large', 'severity', 'medium', 'relation', NULL,
        'message', format('Sort of about %s rows. It may spill to disk. An index in the sort order, or a LIMIT, can avoid it.', node->>'Plan Rows'),
        'filter', node->'Sort Key', 'advice_ddl', NULL));
    END IF;
  END LOOP;
  RETURN res;
END
$f$;

-- ---------------------------------------------------------------------------------------
-- Investigate and prove. Owned by pqn_ledger: they write the ledger, and they call the
-- read functions above, which run as pqn_owner.
-- ---------------------------------------------------------------------------------------
SET LOCAL ROLE pqn_ledger;

-- What one person may keep in the ledger: 256 MiB, counted as whole rows (statement, title, evidence,
-- and the row overhead a payload-only count leaves out: a million payloads of '{}' are 5 MB of payload and
-- 60 MB of table), and 20000 rows. The row limit is also what bounds this function: it reads only this
-- person's rows, and each write is refused before they pass it, so it is a fixed, small scan however
-- large the ledger of everyone else is. Nothing else limits it, so one analyst could otherwise fill the
-- disk of the database everyone else uses. It is a plain function, called by the writers below, which run
-- as pqn_ledger. Two writers at the same moment can each pass and overshoot a little. That is fine for a
-- disk guard.
CREATE FUNCTION pqn_api.ledger_room(l_who text, l_add bigint) RETURNS void
LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $f$
DECLARE
  used bigint;
  n bigint;
BEGIN
  SELECT COALESCE(sum(pg_column_size(i)), 0), count(*) INTO used, n
    FROM pqn_ledger.investigations i WHERE i.who = l_who;
  SELECT used + COALESCE(sum(pg_column_size(e)), 0), n + count(*) INTO used, n
    FROM pqn_ledger.evidence e JOIN pqn_ledger.investigations j ON j.id = e.investigation_id
   WHERE j.who = l_who;
  IF used + l_add > 268435456 THEN
    RAISE EXCEPTION 'pqn: the ledger already holds % of your evidence, and the limit is 256 MiB', pg_size_pretty(used)
      USING HINT = 'Ask an administrator to remove old investigations.';
  END IF;
  IF n >= 20000 THEN
    RAISE EXCEPTION 'pqn: the ledger already holds % of your investigations and evidence rows, and the limit is 20000', n
      USING HINT = 'Ask an administrator to remove old investigations.';
  END IF;
END
$f$;

CREATE FUNCTION pqn_api.investigate(l_query text, l_title text DEFAULT NULL, l_queryid bigint DEFAULT NULL) RETURNS jsonb
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $f$
DECLARE
  p jsonb;
  f jsonb;
  new_id bigint;
BEGIN
  IF current_setting('transaction_read_only') = 'on' THEN
    IF pg_is_in_recovery() THEN
      RAISE EXCEPTION 'pqn: cannot record in a read-only transaction: this server is a standby'
        USING HINT = 'Run investigate on the primary, or use pqn_api.plan and pqn_api.findings on the standby.';
    END IF;
    RAISE EXCEPTION 'pqn: cannot record in a read-only transaction'
      USING HINT = 'pqn_api.run makes the rest of its transaction read only. Call investigate as its own statement.';
  END IF;
  IF l_query IS NULL OR btrim(l_query) = '' THEN
    RAISE EXCEPTION 'pqn: the query text is required';
  END IF;
  p := pqn_api.plan(l_query);
  f := pqn_api.findings(p);
  PERFORM pqn_api.ledger_room(session_user, length(l_query) + pg_column_size(p) + pg_column_size(f));
  INSERT INTO pqn_ledger.investigations (sql, queryid, title)
  VALUES (l_query, l_queryid, left(l_title, 200)) RETURNING id INTO new_id;
  INSERT INTO pqn_ledger.evidence (investigation_id, kind, payload) VALUES (new_id, 'plan', p);
  INSERT INTO pqn_ledger.evidence (investigation_id, kind, payload) VALUES (new_id, 'findings', f);
  RETURN jsonb_build_object(
    'investigation_id', new_id,
    'total_cost', (p->0->'Plan'->>'Total Cost')::numeric,
    'findings', f,
    'advice', COALESCE((SELECT jsonb_agg(x->>'advice_ddl') FROM jsonb_array_elements(f) x WHERE x->>'advice_ddl' IS NOT NULL), '[]'::jsonb));
END
$f$;

-- Prove a candidate: plan both, run both, compare the rows, compare the time, record the verdict.
--   VerifiedEqual + faster   -> Proven
--   VerifiedEqual, not faster -> NotFaster
--   rows differ               -> Different   (never reported as an improvement)
--   $n placeholders           -> Unverified  (plans compared, nothing executed)
--   both return no rows       -> Unverified  (equal, but nothing was compared)
CREATE FUNCTION pqn_api.prove(l_investigation bigint, l_before text, l_after text, l_note text DEFAULT NULL) RETURNS jsonb
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $f$
DECLARE
  owner_of text;
  pb jsonb;
  pa jsonb;
  cost_b numeric;
  cost_a numeric;
  m jsonb;
  verdict text;
  reason text;
  speed numeric;
  result jsonb;
BEGIN
  IF current_setting('transaction_read_only') = 'on' THEN
    IF pg_is_in_recovery() THEN
      RAISE EXCEPTION 'pqn: cannot record in a read-only transaction: this server is a standby'
        USING HINT = 'Use pqn_api.measure_pair on the standby and record the result on the primary.';
    END IF;
    RAISE EXCEPTION 'pqn: cannot record in a read-only transaction'
      USING HINT = 'pqn_api.run makes the rest of its transaction read only. Call prove as its own statement.';
  END IF;
  SELECT i.who INTO owner_of FROM pqn_ledger.investigations i WHERE i.id = l_investigation;
  IF owner_of IS NULL
     OR (owner_of <> session_user AND NOT pg_has_role(session_user, 'pqn_admin', 'MEMBER')) THEN
    RAISE EXCEPTION 'pqn: no investigation % that you may write to', l_investigation;
  END IF;
  -- Before the measuring, not after it. A proof is a few KB, so it is checked without its size.
  PERFORM pqn_api.ledger_room(owner_of, 0);

  pb := pqn_api.plan(l_before);
  pa := pqn_api.plan(l_after);
  cost_b := (pb->0->'Plan'->>'Total Cost')::numeric;
  cost_a := (pa->0->'Plan'->>'Total Cost')::numeric;

  IF pqn_api.has_params(l_before) OR pqn_api.has_params(l_after) THEN
    verdict := 'Unverified';
    reason := 'a statement has $n placeholders and cannot be executed; only the plans were compared';
    m := NULL;
  ELSE
    m := pqn_api.measure_pair(l_before, l_after, 2);
    speed := (m->>'speedup')::numeric;
    IF NOT (m->>'equal')::boolean THEN
      verdict := 'Different';
      reason := 'the two statements returned different rows';
    ELSIF (m->'before'->>'rows')::bigint = 0 AND (m->'after'->>'rows')::bigint = 0 THEN
      -- Two empty results are equal whatever the statements do. Nothing was compared.
      verdict := 'Unverified';
      reason := 'both statements returned no rows, so nothing was compared; try values that return rows';
    ELSIF speed >= 1.2 THEN
      verdict := 'Proven';
      reason := format('same rows, %sx faster', speed);
    ELSE
      verdict := 'NotFaster';
      reason := format('same rows, but only %sx (proof needs at least 1.2x)', COALESCE(speed, 0));
    END IF;
  END IF;

  result := jsonb_build_object(
    'verdict', verdict, 'reason', reason,
    'cost_before', cost_b, 'cost_after', cost_a,
    'measurement', m, 'note', left(l_note, 500),
    'before_sql', l_before, 'after_sql', l_after,
    'source', 'database');
  INSERT INTO pqn_ledger.evidence (investigation_id, kind, payload)
  VALUES (l_investigation, 'proof', result);
  RETURN result;
END
$f$;
RESET ROLE;

-- The 1.0 hint blamed plan() for the read-only transaction. Only run() does that now.
SET LOCAL ROLE pqn_ledger;
CREATE OR REPLACE FUNCTION pqn_api.record_investigation(l_query text, l_queryid bigint DEFAULT NULL,
                                             l_title text DEFAULT NULL) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $f$
DECLARE
  new_id bigint;
BEGIN
  IF current_setting('transaction_read_only') = 'on' THEN
    IF pg_is_in_recovery() THEN
      RAISE EXCEPTION 'pqn: cannot record in a read-only transaction: this server is a standby'
        USING HINT = 'Record on the primary.';
    END IF;
    RAISE EXCEPTION 'pqn: cannot record in a read-only transaction'
      USING HINT = 'pqn_api.run makes the rest of its transaction read only. Call record_* as its own statement, or in a new transaction.';
  END IF;
  IF l_query IS NULL OR btrim(l_query) = '' THEN
    RAISE EXCEPTION 'pqn: the query text is required';
  END IF;
  IF length(l_query) > 100000 THEN
    RAISE EXCEPTION 'pqn: the query text is longer than 100000 characters';
  END IF;
  PERFORM pqn_api.ledger_room(session_user, length(l_query));
  INSERT INTO pqn_ledger.investigations (sql, queryid, title)
  VALUES (l_query, l_queryid, left(l_title, 200))
  RETURNING id INTO new_id;
  RETURN new_id;
END
$f$;

CREATE OR REPLACE FUNCTION pqn_api.record_evidence(l_investigation bigint, l_kind text, l_payload jsonb) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $f$
DECLARE
  owner_of text;
  new_id bigint;
BEGIN
  IF current_setting('transaction_read_only') = 'on' THEN
    IF pg_is_in_recovery() THEN
      RAISE EXCEPTION 'pqn: cannot record in a read-only transaction: this server is a standby'
        USING HINT = 'Record on the primary.';
    END IF;
    RAISE EXCEPTION 'pqn: cannot record in a read-only transaction'
      USING HINT = 'pqn_api.run makes the rest of its transaction read only. Call record_* as its own statement, or in a new transaction.';
  END IF;
  IF l_kind IS NULL OR l_kind !~ '^[a-z][a-z_]{0,39}$' THEN
    RAISE EXCEPTION 'pqn: kind must be lowercase letters and underscores, at most 40 characters';
  END IF;
  IF l_payload IS NULL OR octet_length(l_payload::text) > 1048576 THEN
    RAISE EXCEPTION 'pqn: the payload is required and must be at most 1 MiB';
  END IF;
  SELECT i.who INTO owner_of FROM pqn_ledger.investigations i WHERE i.id = l_investigation;
  IF owner_of IS NULL
     OR (owner_of <> session_user AND NOT pg_has_role(session_user, 'pqn_admin', 'MEMBER')) THEN
    RAISE EXCEPTION 'pqn: no investigation % that you may write to', l_investigation;
  END IF;
  PERFORM pqn_api.ledger_room(owner_of, pg_column_size(l_payload));
  -- A proof stored here was computed by the caller, not by the database. Stamp it so it can never
  -- be mistaken for one prove() computed, whatever the caller put in the payload.
  IF l_kind = 'proof' THEN
    IF jsonb_typeof(l_payload) <> 'object' THEN
      RAISE EXCEPTION 'pqn: a proof must be a JSON object';
    END IF;
    l_payload := l_payload || jsonb_build_object('source', 'client');
  END IF;
  INSERT INTO pqn_ledger.evidence (investigation_id, kind, payload)
  VALUES (l_investigation, l_kind, l_payload)
  RETURNING id INTO new_id;
  RETURN new_id;
END
$f$;
RESET ROLE;

-- ---------------------------------------------------------------------------------------
-- Exposure. expose() takes a scope:
--   view  the caller can read only the listed columns, and plan or measure only over them
--   full  pqn_owner may read the whole table, so statements that touch any column can be
--         planned and measured, while run() still returns only the listed columns.
--         Plans and row counts over a hidden column can reveal how common a value is. The DBA
--         chooses this per table, and verify_setup() lists it.
-- ---------------------------------------------------------------------------------------
DROP FUNCTION pqn_api.expose(regclass, text[], text);
DROP FUNCTION pqn_api.expose_sql(regclass, text[], text);

CREATE FUNCTION pqn_api.expose_sql(rel regclass, cols text[], view_name text DEFAULT NULL, scope text DEFAULT 'view') RETURNS text
LANGUAGE plpgsql AS $f$
DECLARE
  nsp name;
  relname name;
  kind "char";
  v name;
  collist text;
  missing text;
  grant_line text;
BEGIN
  IF scope NOT IN ('view', 'full') THEN
    RAISE EXCEPTION 'pqn: scope must be view or full';
  END IF;
  SELECT n.nspname, c.relname, c.relkind INTO nsp, relname, kind
    FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE c.oid = rel;
  IF kind NOT IN ('r', 'p', 'v', 'm', 'f') THEN
    RAISE EXCEPTION 'pqn: % is not a table or view', rel;
  END IF;
  IF nsp IN ('pqn', 'pqn_api', 'pqn_ledger', 'information_schema') OR nsp LIKE 'pg\_%' THEN
    RAISE EXCEPTION 'pqn: schema % cannot be exposed', nsp;
  END IF;
  IF cols IS NULL OR cardinality(cols) = 0 THEN
    RAISE EXCEPTION 'pqn: list the columns to expose';
  END IF;
  SELECT string_agg(x, ', ') INTO missing
    FROM unnest(cols) AS x
   WHERE NOT EXISTS (SELECT 1 FROM pg_attribute a
                      WHERE a.attrelid = rel AND a.attname = x AND a.attnum > 0 AND NOT a.attisdropped);
  IF missing IS NOT NULL THEN
    RAISE EXCEPTION 'pqn: % has no column(s) %', rel, missing;
  END IF;
  v := COALESCE(NULLIF(view_name, ''), relname);
  IF EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
              WHERE n.nspname = 'pqn' AND c.relname = v) THEN
    RAISE EXCEPTION 'pqn: view pqn.% already exists', v;
  END IF;
  SELECT string_agg(quote_ident(x), ', ') INTO collist FROM unnest(cols) AS x;
  grant_line := CASE scope
    WHEN 'full' THEN format('GRANT SELECT ON %I.%I TO pqn_owner;  -- scope full: plans and row counts cover every column', nsp, relname)
    ELSE format('GRANT SELECT (%s) ON %I.%I TO pqn_owner;', collist, nsp, relname) END;
  RETURN array_to_string(ARRAY[
    format('GRANT USAGE ON SCHEMA %I TO pqn_owner;', nsp),
    grant_line,
    format('CREATE VIEW pqn.%I AS SELECT %s FROM %I.%I;  -- as pqn_owner', v, collist, nsp, relname),
    format('GRANT SELECT ON pqn.%I TO pqn_reader;', v),
    format('INSERT INTO pqn.exposed (schema_name, table_name, view_name, scope, columns) VALUES (%L, %L, %L, %L, %L);  -- as pqn_owner',
           nsp, relname, v, scope, cols::text)], E'\n');
END
$f$;

CREATE FUNCTION pqn_api.expose(rel regclass, cols text[], view_name text DEFAULT NULL, scope text DEFAULT 'view') RETURNS text
LANGUAGE plpgsql AS $f$
DECLARE
  script text := pqn_api.expose_sql(rel, cols, view_name, scope);
  line text;
  saved name := current_user;
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = 'pqn') THEN
    RAISE EXCEPTION 'pqn: run pqn_api.init() first';
  END IF;
  FOREACH line IN ARRAY string_to_array(script, E'\n') LOOP
    IF line LIKE 'CREATE VIEW%' OR line LIKE 'INSERT INTO pqn.exposed%' THEN
      EXECUTE 'SET LOCAL ROLE pqn_owner';
      EXECUTE regexp_replace(line, '\s*--.*$', '');
      EXECUTE format('SET LOCAL ROLE %I', saved);
    ELSE
      EXECUTE regexp_replace(line, '\s*--.*$', '');
    END IF;
  END LOOP;
  -- The view runs as pqn_owner (SECURITY of the underlying table, not a
  -- SECURITY DEFINER view), so row-level security on the source table applies
  -- to every read through it. A table with RLS enabled but no policy naming
  -- pqn_owner (or public) silently returns zero rows for every caller — the
  -- exact shape of "no findings" a query legitimately returning no data also
  -- produces, so this is easy to mistake for "the query found nothing" rather
  -- than "the exposure cannot read anything." Flag it up front instead.
  IF EXISTS (SELECT 1 FROM pg_class WHERE oid = rel AND relrowsecurity)
     -- Table owners bypass RLS entirely unless FORCE ROW LEVEL SECURITY is
     -- set, regardless of policies. pqn_owner owning the exposed relation
     -- (a real deployment shape outside this repo's own demo tables, where
     -- pqn_owner never owns anything) reads fine without any covering
     -- policy — skip the notice in that case instead of a false alarm.
     AND NOT EXISTS (
       SELECT 1 FROM pg_class c
       WHERE c.oid = rel AND c.relowner = 'pqn_owner'::regrole AND NOT c.relforcerowsecurity
     )
     AND NOT EXISTS (
       SELECT 1 FROM pg_policies p
       WHERE (p.schemaname, p.tablename) = (SELECT n.nspname, c.relname
                                             FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
                                             WHERE c.oid = rel)
         AND p.cmd IN ('ALL', 'SELECT')
         AND (
           'public' = ANY(p.roles)
           -- pg_has_role(..., 'member') matches direct or inherited group
           -- membership, not just an exact role-name hit in p.roles — a
           -- policy granted to a group role pqn_owner belongs to still
           -- applies to reads through the view, per how PostgreSQL itself
           -- resolves RLS policy applicability.
           OR EXISTS (SELECT 1 FROM unnest(p.roles) g WHERE pg_has_role('pqn_owner', g, 'member'))
         )
     ) THEN
    RAISE NOTICE 'pqn: % has row-level security enabled but no policy applies to pqn_owner (or public) — the new view may return 0 rows for every caller regardless of query. Add a policy for pqn_owner, or confirm this is intended.', rel::text;
  END IF;
  RETURN script;
END
$f$;

-- Undo expose(): drop the view and the registry row, then give pqn_owner exactly the access the
-- remaining views on that table need (none when this was the last).
CREATE FUNCTION pqn_api.unexpose(l_view name) RETURNS text
LANGUAGE plpgsql AS $f$
DECLARE
  r record;
  saved name := current_user;
  script text := '';
  cols text;
BEGIN
  SELECT * INTO r FROM pqn_api.exposed() e WHERE e.view_name = l_view;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'pqn: no exposed view called %', l_view;
  END IF;
  EXECUTE 'SET LOCAL ROLE pqn_owner';
  EXECUTE format('DROP VIEW pqn.%I', l_view);
  EXECUTE format('DELETE FROM pqn.exposed WHERE view_name = %L', l_view);
  EXECUTE format('SET LOCAL ROLE %I', saved);
  script := format('DROP VIEW pqn.%I;', l_view);
  -- pqn_owner's access to the table is the union of what the views that remain need. Removing one
  -- view must not leave its columns readable, so take everything back and grant that union again.
  EXECUTE format('REVOKE ALL ON %I.%I FROM pqn_owner', r.schema_name, r.table_name);
  script := script || E'\n' || format('REVOKE ALL ON %I.%I FROM pqn_owner;', r.schema_name, r.table_name);
  IF EXISTS (SELECT 1 FROM pqn_api.exposed() e
              WHERE e.schema_name = r.schema_name AND e.table_name = r.table_name AND e.scope = 'full') THEN
    EXECUTE format('GRANT SELECT ON %I.%I TO pqn_owner', r.schema_name, r.table_name);
    script := script || E'\n' || format('GRANT SELECT ON %I.%I TO pqn_owner;', r.schema_name, r.table_name);
  ELSE
    SELECT string_agg(quote_ident(c), ', ' ORDER BY c) INTO cols
      FROM (SELECT DISTINCT unnest(e.columns) AS c FROM pqn_api.exposed() e
             WHERE e.schema_name = r.schema_name AND e.table_name = r.table_name) x;
    IF cols IS NOT NULL THEN
      EXECUTE format('GRANT SELECT (%s) ON %I.%I TO pqn_owner', cols, r.schema_name, r.table_name);
      script := script || E'\n' || format('GRANT SELECT (%s) ON %I.%I TO pqn_owner;', cols, r.schema_name, r.table_name);
    END IF;
  END IF;
  RETURN script;
END
$f$;

-- ---------------------------------------------------------------------------------------
-- Grants. Nothing here reaches PUBLIC.
-- ---------------------------------------------------------------------------------------
REVOKE CREATE ON SCHEMA pqn_api FROM pqn_owner, pqn_reader, pqn_stats, pqn_ledger;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA pqn_api FROM PUBLIC;

-- The ledger role calls the read functions from inside investigate and prove.
GRANT USAGE ON SCHEMA pqn_api TO pqn_ledger;
GRANT EXECUTE ON FUNCTION pqn_api.plan(text), pqn_api.measure_pair(text, text, integer),
                          pqn_api.findings(jsonb), pqn_api.has_params(text) TO pqn_ledger;
-- The owner role calls its own helper from inside plan and measure_pair.
GRANT USAGE ON SCHEMA pqn_api TO pqn_owner;
GRANT EXECUTE ON FUNCTION pqn_api.exposed_path(), pqn_api.explain_ms(text, text) TO pqn_owner;

GRANT EXECUTE ON FUNCTION pqn_api.measure_pair(text, text, integer), pqn_api.findings(jsonb),
                          pqn_api.investigate(text, text, bigint),
                          pqn_api.prove(bigint, text, text, text) TO pqn_analyst;
GRANT EXECUTE ON FUNCTION pqn_api.expose(regclass, text[], text, text),
                          pqn_api.expose_sql(regclass, text[], text, text),
                          pqn_api.unexpose(name), pqn_api.exposed(),
                          pqn_api.record_limit(name, bigint, bigint, bigint), pqn_api.enforce_limits() TO pqn_admin;

-- ---------------------------------------------------------------------------------------
-- verify_setup(), extended for the new functions and for exposure scopes
-- ---------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION pqn_api.verify_setup()
RETURNS TABLE (level text, check_name text, detail text, fix text)
LANGUAGE plpgsql STABLE AS $f$
DECLARE
  r record;
  bad boolean;
  owner_roles text[] := ARRAY['pqn_owner', 'pqn_reader', 'pqn_stats', 'pqn_ledger'];
  ro name;
  has_ledger boolean := EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
                                 WHERE n.nspname = 'pqn_ledger' AND c.relname = 'investigations');
  pss oid := (SELECT e.oid FROM pg_extension e WHERE e.extname = 'pg_stat_statements');
  db_oid oid := (SELECT d.oid FROM pg_database d WHERE d.datname = current_database());
  sensitive text := '(ssn|password|passwd|secret|token|api_?key|credit|card|iban|salary|dob|birth|email|phone)';
  n_limits bigint := 0;
  unrecorded text;
BEGIN
  -- 1. The owner roles exist and are plain NOLOGIN roles.
  bad := false;
  FOREACH ro IN ARRAY owner_roles LOOP
    SELECT * INTO r FROM pg_roles x WHERE x.rolname = ro;
    IF NOT FOUND THEN
      bad := true; level := 'BLOCK'; check_name := 'owner role exists';
      detail := format('role %s is missing', ro); fix := 'run pqn-roles.sql'; RETURN NEXT;
    ELSIF r.rolcanlogin OR r.rolsuper OR r.rolcreaterole OR r.rolcreatedb OR r.rolbypassrls OR r.rolreplication THEN
      bad := true; level := 'BLOCK'; check_name := 'owner role is unprivileged';
      detail := format('%s can log in or holds a powerful attribute', ro);
      fix := format('ALTER ROLE %I NOLOGIN NOSUPERUSER NOCREATEROLE NOCREATEDB NOBYPASSRLS NOREPLICATION', ro); RETURN NEXT;
    END IF;
  END LOOP;
  IF NOT bad THEN
    level := 'OK'; check_name := 'owner roles'; detail := 'four NOLOGIN roles without powerful attributes'; fix := NULL; RETURN NEXT;
  END IF;

  -- 2. Every SECURITY DEFINER function is owned by the narrow role meant for it.
  bad := false;
  FOR r IN
    SELECT p.oid::regprocedure::text AS fn, pg_get_userbyid(p.proowner)::text AS owner_name, e.expected
      FROM pg_proc p
      JOIN pg_namespace n ON n.oid = p.pronamespace
      LEFT JOIN (VALUES ('plan', 'pqn_owner'), ('run', 'pqn_reader'), ('top', 'pqn_stats'),
                        ('record_investigation', 'pqn_ledger'), ('record_evidence', 'pqn_ledger'),
                        ('investigations', 'pqn_ledger'), ('evidence', 'pqn_ledger'),
                        ('measure_pair', 'pqn_owner'), ('explain_ms', 'pqn_owner'), ('record_limit', 'pqn_owner'), ('exposed_path', 'pqn_owner'), ('exposed', 'pqn_owner'),
                        ('investigate', 'pqn_ledger'), ('prove', 'pqn_ledger')) e(fname, expected)
             ON e.fname = p.proname
     WHERE n.nspname = 'pqn_api' AND p.prosecdef
  LOOP
    IF r.expected IS NULL OR r.owner_name <> r.expected THEN
      bad := true; level := 'BLOCK'; check_name := 'definer function ownership';
      detail := format('%s is owned by %s, expected %s', r.fn, r.owner_name, COALESCE(r.expected, 'nobody (unexpected definer function)'));
      fix := CASE WHEN r.expected IS NULL THEN format('DROP FUNCTION %s', r.fn)
                  ELSE format('ALTER FUNCTION %s OWNER TO %I', r.fn, r.expected) END;
      RETURN NEXT;
    END IF;
  END LOOP;
  IF NOT bad THEN
    level := 'OK'; check_name := 'definer function ownership'; detail := 'each definer function is owned by its narrow role'; fix := NULL; RETURN NEXT;
  END IF;

  -- 3. Every definer function pins its search_path.
  bad := false;
  FOR r IN
    SELECT p.oid::regprocedure::text AS fn
      FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
     WHERE n.nspname = 'pqn_api' AND p.prosecdef
       AND NOT EXISTS (SELECT 1 FROM unnest(COALESCE(p.proconfig, '{}')) c WHERE c LIKE 'search_path=%')
  LOOP
    bad := true; level := 'BLOCK'; check_name := 'definer search_path';
    detail := format('%s does not pin search_path', r.fn);
    fix := format('ALTER FUNCTION %s SET search_path = pg_catalog, pg_temp', r.fn); RETURN NEXT;
  END LOOP;
  IF NOT bad THEN
    level := 'OK'; check_name := 'definer search_path'; detail := 'pinned on every definer function'; fix := NULL; RETURN NEXT;
  END IF;

  -- 3b. The pinned search_path of a definer function names only schemas we control.
  bad := false;
  FOR r IN
    SELECT p.oid::regprocedure::text AS fn, c AS setting
      FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace, unnest(COALESCE(p.proconfig, '{}')) c
     WHERE n.nspname = 'pqn_api' AND p.prosecdef AND c LIKE 'search_path=%'
       AND EXISTS (SELECT 1 FROM unnest(string_to_array(substr(c, 13), ',')) s
                    WHERE btrim(s) NOT IN ('pg_catalog', 'pqn', 'pg_temp'))
  LOOP
    bad := true; level := 'BLOCK'; check_name := 'definer search_path scope';
    detail := format('%s runs with %s, which names a schema outside pg_catalog, pqn and pg_temp', r.fn, r.setting);
    fix := format('ALTER FUNCTION %s SET search_path = pg_catalog, pg_temp', r.fn); RETURN NEXT;
  END LOOP;
  IF NOT bad THEN
    level := 'OK'; check_name := 'definer search_path scope'; detail := 'only pg_catalog, pqn and pg_temp'; fix := NULL; RETURN NEXT;
  END IF;

  -- 4. Nothing is granted to PUBLIC: no function, and nothing in the ledger.
  bad := false;
  FOR r IN
    SELECT p.oid::regprocedure::text AS fn
      FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace,
           LATERAL aclexplode(COALESCE(p.proacl, acldefault('f', p.proowner))) a
     WHERE n.nspname = 'pqn_api' AND a.grantee = 0 AND a.privilege_type = 'EXECUTE'
  LOOP
    bad := true; level := 'BLOCK'; check_name := 'PUBLIC execute';
    detail := format('PUBLIC can execute %s', r.fn); fix := format('REVOKE ALL ON FUNCTION %s FROM PUBLIC', r.fn); RETURN NEXT;
  END LOOP;
  FOR r IN
    SELECT format('%I.%I', n.nspname, c.relname) AS obj
      FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace,
           LATERAL aclexplode(COALESCE(c.relacl, acldefault('r', c.relowner))) a
     WHERE n.nspname = 'pqn_ledger' AND c.relkind IN ('r', 'p') AND a.grantee = 0
    UNION ALL
    SELECT format('schema %I', n.nspname)
      FROM pg_namespace n, LATERAL aclexplode(COALESCE(n.nspacl, acldefault('n', n.nspowner))) a
     WHERE n.nspname = 'pqn_ledger' AND a.grantee = 0
  LOOP
    bad := true; level := 'BLOCK'; check_name := 'PUBLIC ledger access';
    detail := format('PUBLIC holds a privilege on %s', r.obj); fix := format('REVOKE ALL ON %s FROM PUBLIC', r.obj); RETURN NEXT;
  END LOOP;
  IF NOT bad THEN
    level := 'OK'; check_name := 'PUBLIC'; detail := 'holds no function and no ledger privilege'; fix := NULL; RETURN NEXT;
  END IF;

  -- 5. pqn_reader reaches only schema pqn and can write nothing.
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'pqn_reader') THEN
    bad := false;
    FOR r IN
      SELECT format('%I.%I', n.nspname, c.relname) AS obj
        FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
       WHERE c.relkind IN ('r', 'p', 'v', 'm', 'f')
         AND n.nspname NOT LIKE 'pg\_%' AND n.nspname NOT IN ('information_schema', 'pqn')
         AND has_any_column_privilege('pqn_reader', c.oid, 'SELECT')
         AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_class'::regclass AND d.objid = c.oid
                            AND d.deptype = 'e' AND d.refobjid = COALESCE(pss, 0))
       ORDER BY 1 LIMIT 10
    LOOP
      bad := true; level := 'BLOCK'; check_name := 'reader reach';
      detail := format('pqn_reader can read %s, which is outside schema pqn', r.obj);
      fix := format('REVOKE SELECT ON %s FROM pqn_reader, PUBLIC (check the grantee)', r.obj); RETURN NEXT;
    END LOOP;
    FOR r IN
      SELECT format('%I.%I', n.nspname, c.relname) AS obj
        FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
       WHERE c.relkind IN ('r', 'p', 'v', 'm', 'f') AND n.nspname NOT LIKE 'pg\_%' AND n.nspname <> 'information_schema'
         AND (has_table_privilege('pqn_reader', c.oid, 'INSERT,UPDATE,DELETE,TRUNCATE')
              OR has_any_column_privilege('pqn_reader', c.oid, 'INSERT,UPDATE'))
       ORDER BY 1 LIMIT 10
    LOOP
      bad := true; level := 'BLOCK'; check_name := 'reader can write';
      detail := format('pqn_reader can modify %s', r.obj);
      fix := format('REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON %s FROM pqn_reader, PUBLIC', r.obj); RETURN NEXT;
    END LOOP;
    FOR r IN
      SELECT n.nspname::text AS s FROM pg_namespace n
       WHERE n.nspname NOT LIKE 'pg\_%' AND n.nspname <> 'information_schema'
         AND has_schema_privilege('pqn_reader', n.oid, 'CREATE')
    LOOP
      bad := true; level := 'BLOCK'; check_name := 'reader can create objects';
      detail := format('pqn_reader has CREATE on schema %s', r.s);
      fix := format('REVOKE CREATE ON SCHEMA %I FROM pqn_reader, PUBLIC', r.s); RETURN NEXT;
    END LOOP;
    IF has_database_privilege('pqn_reader', current_database(), 'CREATE') THEN
      bad := true; level := 'BLOCK'; check_name := 'reader can create schemas';
      detail := 'pqn_reader has CREATE on the database';
      fix := format('REVOKE CREATE ON DATABASE %I FROM pqn_reader, PUBLIC', current_database()); RETURN NEXT;
    END IF;
    IF NOT bad THEN
      level := 'OK'; check_name := 'reader reach'; detail := 'pqn_reader reads only schema pqn and writes nothing'; fix := NULL; RETURN NEXT;
    END IF;
    IF pss IS NOT NULL AND EXISTS (SELECT 1 FROM pg_depend d JOIN pg_class c ON c.oid = d.objid
                                    WHERE d.classid = 'pg_class'::regclass AND d.refobjid = pss AND d.deptype = 'e'
                                      AND c.relname = 'pg_stat_statements' AND has_any_column_privilege('pqn_reader', c.oid, 'SELECT')) THEN
      level := 'WARN'; check_name := 'reader sees pg_stat_statements';
      detail := 'PUBLIC can read pg_stat_statements, so analyst SQL sees the statements that ran as pqn_reader, which is every analyst''s pqn_api.run() traffic';
      fix := 'REVOKE SELECT ON pg_stat_statements, pg_stat_statements_info FROM PUBLIC (affects every role outside pg_read_all_stats)'; RETURN NEXT;
    END IF;
    IF has_database_privilege('pqn_reader', current_database(), 'TEMP') THEN
      level := 'WARN'; check_name := 'reader TEMP privilege';
      detail := 'pqn_reader may create temporary tables (PUBLIC holds TEMP by default). The read-only transaction blocks it, and this is defence in depth only';
      fix := format('REVOKE TEMP ON DATABASE %I FROM PUBLIC (affects every role)', current_database()); RETURN NEXT;
    END IF;
  END IF;

  -- 6. pqn_owner may only read, and only outside the ledger.
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'pqn_owner') THEN
    bad := false;
    FOR r IN
      SELECT format('%I.%I', n.nspname, c.relname) AS obj
        FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
       WHERE c.relkind IN ('r', 'p', 'v', 'm', 'f')
         AND n.nspname NOT LIKE 'pg\_%' AND n.nspname NOT IN ('information_schema', 'pqn')
         AND (has_table_privilege('pqn_owner', c.oid, 'INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER')
              OR has_any_column_privilege('pqn_owner', c.oid, 'INSERT,UPDATE,REFERENCES')
              OR (n.nspname = 'pqn_ledger' AND has_any_column_privilege('pqn_owner', c.oid, 'SELECT')))
       ORDER BY 1 LIMIT 10
    LOOP
      bad := true; level := 'BLOCK'; check_name := 'owner privileges';
      detail := format('pqn_owner holds more than SELECT on %s, or can read the ledger', r.obj);
      fix := format('REVOKE ALL ON %s FROM pqn_owner', r.obj); RETURN NEXT;
    END LOOP;
    IF NOT bad THEN
      level := 'OK'; check_name := 'owner privileges'; detail := 'pqn_owner holds SELECT only, on exposed columns'; fix := NULL; RETURN NEXT;
    END IF;
  END IF;

  -- 6b. Nobody who can log in should be able to become an owner role.
  bad := false;
  FOR r IN
    WITH RECURSIVE tree(member, via) AS (
      SELECT am.member, g.rolname::text FROM pg_auth_members am JOIN pg_roles g ON g.oid = am.roleid
       WHERE g.rolname = ANY (owner_roles)
      UNION
      SELECT am.member, t.via FROM pg_auth_members am JOIN tree t ON am.roleid = t.member)
    SELECT DISTINCT x.rolname::text AS rolname, t.via FROM tree t JOIN pg_roles x ON x.oid = t.member
     WHERE x.rolcanlogin AND NOT x.rolsuper ORDER BY 1, 2
  LOOP
    bad := true; level := 'WARN'; check_name := 'login can become an owner role';
    detail := format('%s is a member of %s and can act with its privileges. Installers need this only during install and init()', r.rolname, r.via);
    fix := format('REVOKE %I FROM %I', r.via, r.rolname); RETURN NEXT;
  END LOOP;
  IF NOT bad THEN
    level := 'OK'; check_name := 'owner role membership'; detail := 'no login role can become an owner role'; fix := NULL; RETURN NEXT;
  END IF;

  -- 7. People who can log in and reach the API: limits on their own login role.
  bad := false;
  FOR r IN
    WITH RECURSIVE tree(member) AS (
      SELECT am.member FROM pg_auth_members am JOIN pg_roles g ON g.oid = am.roleid
       WHERE g.rolname IN ('pqn_viewer', 'pqn_analyst', 'pqn_admin')
      UNION
      SELECT am.member FROM pg_auth_members am JOIN tree t ON am.roleid = t.member)
    SELECT DISTINCT x.rolname::text AS rolname, x.rolsuper,
           EXISTS (SELECT 1 FROM pg_db_role_setting s, unnest(s.setconfig) c
                    WHERE s.setrole IN (x.oid, 0) AND s.setdatabase IN (0, db_oid)
                      AND c ~ '^statement_timeout=' AND c !~ '^statement_timeout=0(ms|s|min)?$') AS has_stmt,
           EXISTS (SELECT 1 FROM pg_db_role_setting s, unnest(s.setconfig) c
                    WHERE s.setrole IN (x.oid, 0) AND s.setdatabase IN (0, db_oid)
                      AND c ~ '^lock_timeout=' AND c !~ '^lock_timeout=0(ms|s|min)?$') AS has_lock,
           EXISTS (SELECT 1 FROM pg_db_role_setting s, unnest(s.setconfig) c
                    WHERE s.setrole IN (x.oid, 0) AND s.setdatabase IN (0, db_oid)
                      AND c ~ '^idle_in_transaction_session_timeout=' AND c !~ '^idle_in_transaction_session_timeout=0(ms|s|min)?$') AS has_idle
      FROM tree t JOIN pg_roles x ON x.oid = t.member
     WHERE x.rolcanlogin
     ORDER BY 1
  LOOP
    IF NOT r.has_stmt THEN
      bad := true; level := 'BLOCK'; check_name := 'login has no statement_timeout';
      detail := format('%s can run queries with no time limit. A limit on a group role does nothing, it must be on the login role', r.rolname);
      fix := format('ALTER ROLE %I SET statement_timeout = ''15s''  (or SELECT pqn_api.enroll(%L))', r.rolname, r.rolname); RETURN NEXT;
    END IF;
    IF NOT (r.has_lock AND r.has_idle) THEN
      bad := true; level := 'WARN'; check_name := 'login has no lock or idle timeout';
      detail := format('%s has no lock_timeout or idle_in_transaction_session_timeout', r.rolname);
      fix := format('SELECT pqn_api.enroll(%L)', r.rolname); RETURN NEXT;
    END IF;
    IF r.rolsuper THEN
      bad := true; level := 'WARN'; check_name := 'superuser in a pqn group';
      detail := format('%s is a superuser, so this boundary does not bind them', r.rolname);
      fix := format('REVOKE pqn_viewer, pqn_analyst, pqn_admin FROM %I', r.rolname); RETURN NEXT;
    END IF;
  END LOOP;
  IF NOT bad THEN
    level := 'OK'; check_name := 'member limits'; detail := 'every member has statement, lock and idle timeouts'; fix := NULL; RETURN NEXT;
  END IF;

  -- 7b. Planning and measuring resolve table names through the schemas of exposed tables, and
  -- run as pqn_owner. A member who can create objects in one of those schemas could plant a
  -- function that a plan calls with pqn_owner's rights.
  bad := false;
  FOR r IN
    WITH RECURSIVE tree(member) AS (
      SELECT am.member FROM pg_auth_members am JOIN pg_roles g ON g.oid = am.roleid
       WHERE g.rolname IN ('pqn_viewer', 'pqn_analyst', 'pqn_admin')
      UNION
      SELECT am.member FROM pg_auth_members am JOIN tree t ON am.roleid = t.member),
    planning AS (
      SELECT e.schema_name AS s FROM pqn_api.exposed() e
      UNION
      SELECT 'public'::name WHERE NOT EXISTS (SELECT 1 FROM pqn_api.exposed()))
    SELECT DISTINCT x.rolname::text AS rolname, p.s::text AS s
      FROM tree t JOIN pg_roles x ON x.oid = t.member
      CROSS JOIN planning p JOIN pg_namespace ns ON ns.nspname = p.s
     WHERE x.rolcanlogin AND NOT x.rolsuper AND has_schema_privilege(x.oid, ns.oid, 'CREATE')
     ORDER BY 1, 2
  LOOP
    bad := true; level := 'BLOCK'; check_name := 'member can create objects in a planning schema';
    detail := format('%s can create objects in schema %s, which plans and measurements resolve names through', r.rolname, r.s);
    fix := format('REVOKE CREATE ON SCHEMA %I FROM %I, PUBLIC', r.s, r.rolname); RETURN NEXT;
  END LOOP;
  IF NOT bad THEN
    level := 'OK'; check_name := 'planning schemas'; detail := 'no member can create objects in a schema that plans resolve names through'; fix := NULL; RETURN NEXT;
  END IF;

  -- 7c. Tables exposed with scope full.
  FOR r IN SELECT format('%I.%I', e.schema_name, e.table_name) AS obj, e.view_name::text AS vname
             FROM pqn_api.exposed() e WHERE e.scope = 'full' ORDER BY 1
  LOOP
    level := 'WARN'; check_name := 'table exposed with scope full';
    detail := format('%s: plans and row counts cover every column, though view pqn.%s shows only some. A value in a hidden column can be probed through the row counts', r.obj, r.vname);
    fix := format('SELECT pqn_api.unexpose(%L), then expose it again with scope view', r.vname); RETURN NEXT;
  END LOOP;

  -- 8. Members hold nothing directly on the ledger.
  IF has_ledger THEN
    bad := false;
    FOR r IN
      WITH RECURSIVE tree(member) AS (
        SELECT am.member FROM pg_auth_members am JOIN pg_roles g ON g.oid = am.roleid
         WHERE g.rolname IN ('pqn_viewer', 'pqn_analyst', 'pqn_admin')
        UNION
        SELECT am.member FROM pg_auth_members am JOIN tree t ON am.roleid = t.member)
      SELECT DISTINCT x.rolname::text AS rolname, format('%I.%I', n.nspname, c.relname) AS obj
        FROM tree t JOIN pg_roles x ON x.oid = t.member
        CROSS JOIN pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
       WHERE NOT x.rolsuper AND n.nspname = 'pqn_ledger' AND c.relkind IN ('r', 'p')
         AND has_table_privilege(x.oid, c.oid, 'SELECT,INSERT,UPDATE,DELETE,TRUNCATE')
    LOOP
      bad := true; level := 'BLOCK'; check_name := 'member ledger access';
      detail := format('%s holds a direct privilege on %s', r.rolname, r.obj);
      fix := format('REVOKE ALL ON %s FROM %I', r.obj, r.rolname); RETURN NEXT;
    END LOOP;
    IF NOT bad THEN
      level := 'OK'; check_name := 'member ledger access'; detail := 'members reach the ledger only through the functions'; fix := NULL; RETURN NEXT;
    END IF;
  ELSE
    level := 'WARN'; check_name := 'ledger'; detail := 'the ledger does not exist yet'; fix := 'SELECT pqn_api.init()'; RETURN NEXT;
  END IF;

  -- 9. Curated views: columns whose names look sensitive.
  bad := false;
  FOR r IN
    SELECT format('pqn.%I.%I', c.relname, a.attname) AS col
      FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
      JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
     WHERE n.nspname = 'pqn' AND c.relkind IN ('v', 'm') AND a.attname ~* sensitive
     ORDER BY 1
  LOOP
    bad := true; level := 'WARN'; check_name := 'sensitive-looking column exposed';
    detail := format('%s is visible to analysts', r.col); fix := 'drop the view and expose it again without that column'; RETURN NEXT;
  END LOOP;
  IF NOT bad THEN
    level := 'OK'; check_name := 'exposed columns'; detail := 'no column name looks sensitive'; fix := NULL; RETURN NEXT;
  END IF;

  -- 10. Environment.
  IF NOT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pg_stat_statements') THEN
    level := 'WARN'; check_name := 'pg_stat_statements'; detail := 'not installed here, so pqn_api.top() cannot work';
    fix := 'CREATE EXTENSION pg_stat_statements (also needs shared_preload_libraries)'; RETURN NEXT;
  END IF;
  -- Limits: the per-login timeout is a session default a person can lift, so it is enforced from
  -- outside by enforce_limits(). Say so, and name anyone enrolled before limits were recorded.
  IF to_regclass('pqn.limits') IS NOT NULL THEN
    SELECT count(*) INTO n_limits FROM pqn.limits;
    SELECT string_agg(DISTINCT m.rolname::text, ', ' ORDER BY m.rolname::text) INTO unrecorded
      FROM pg_auth_members am
      JOIN pg_roles g ON g.oid = am.roleid AND g.rolname IN ('pqn_viewer', 'pqn_analyst', 'pqn_admin')
      JOIN pg_roles m ON m.oid = am.member AND m.rolcanlogin AND m.rolname !~ '^pqn_'
     WHERE NOT EXISTS (SELECT 1 FROM pqn.limits l WHERE l.login = m.rolname);
    IF unrecorded IS NOT NULL THEN
      level := 'WARN'; check_name := 'limit not recorded';
      detail := format('%s enrolled without a recorded limit, so enforce_limits() cannot apply one', unrecorded);
      fix := 'SELECT pqn_api.enroll(login, group) again for each of them'; RETURN NEXT;
    END IF;
    level := 'INFO'; check_name := 'limits enforcement';
    detail := format('%s login(s) have recorded statement, lock and idle-in-transaction limits. A person can lift any of these for themselves, so they are enforced from outside: run pqn_api.enforce_limits() every few seconds from pg_cron or cron', n_limits);
    fix := NULL; RETURN NEXT;
  END IF;
  IF current_setting('server_version_num')::integer < 160000 THEN
    level := 'WARN'; check_name := 'server version'; detail := 'older than PostgreSQL 16: pqn_api.plan() cannot plan statements with $n placeholders';
    fix := 'upgrade to PostgreSQL 16 or later'; RETURN NEXT;
  END IF;
  IF pg_is_in_recovery() THEN
    level := 'INFO'; check_name := 'standby'; detail := 'this server is a hot standby: plan, run, findings, measure_pair and reading the ledger work here. record_*, investigate and prove fail here (run them on the primary), and top() shows only the statements that ran on this server';
    fix := NULL; RETURN NEXT;
  END IF;
END
$f$;
