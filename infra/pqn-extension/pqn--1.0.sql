\echo Use "CREATE EXTENSION pqn" to load this file. \quit

-- PgQueryNarrative "pqn" extension 1.0.
--
-- Everything people can call lives in schema pqn_api. The extension writes only to its own
-- ledger (schema pqn_ledger, created by pqn_api.init(), not by this script, so that
-- DROP EXTENSION removes the code and keeps the evidence). It reads user data only through
-- views the DBA chose (schema pqn).
--
-- A superuser can run CREATE EXTENSION pqn directly: the roles are created if they are missing.
-- Anyone else runs pqn-roles.sql first. The script creates each function as the installer and then hands
-- it to the narrowest role that can do its job. A SECURITY DEFINER function runs with its
-- owner's rights, so no definer function may be owned by the installer or a superuser.

DO $chk$
DECLARE
  r text;
  missing text[] := '{}';
BEGIN
  FOREACH r IN ARRAY ARRAY['pqn_owner', 'pqn_reader', 'pqn_stats', 'pqn_ledger',
                           'pqn_viewer', 'pqn_analyst', 'pqn_admin'] LOOP
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = r) THEN
      missing := missing || r;
    END IF;
  END LOOP;

  IF cardinality(missing) > 0 THEN
    -- A superuser can create the roles, so CREATE EXTENSION alone is enough. Anyone else needs a
    -- DBA to run pqn-roles.sql first, because creating roles takes rights an ordinary role lacks.
    IF NOT (SELECT rolsuper FROM pg_roles WHERE rolname = current_user) THEN
      RAISE EXCEPTION 'pqn: role % does not exist. Run pqn-roles.sql first, or create the extension as a superuser.', missing[1]
        USING HINT = 'psql -d ' || current_database() || ' -v installer=' || current_user || ' -f pqn-roles.sql';
    END IF;
    FOREACH r IN ARRAY missing LOOP
      EXECUTE format('CREATE ROLE %I NOLOGIN', r);
    END LOOP;
    GRANT pg_monitor TO pqn_stats;
    GRANT pqn_viewer TO pqn_analyst;
    GRANT pqn_analyst TO pqn_admin;
  END IF;

  FOREACH r IN ARRAY ARRAY['pqn_owner', 'pqn_reader', 'pqn_stats', 'pqn_ledger'] LOOP
    IF NOT pg_has_role(current_user, r, 'MEMBER') THEN
      RAISE EXCEPTION 'pqn: % must be a member of % to hand objects over. Run pqn-roles.sql with -v installer=%.',
        current_user, r, current_user;
    END IF;
  END LOOP;
END;
$chk$;

-- The new owner needs CREATE on the schema while ALTER ... OWNER runs. Revoked at the end.
GRANT CREATE ON SCHEMA pqn_api TO pqn_owner, pqn_reader, pqn_stats, pqn_ledger;

-- ---------------------------------------------------------------------------------------
-- Reading: estimated plans, analyst SQL, workload statistics
-- ---------------------------------------------------------------------------------------

-- Estimated plan for one statement, without running it. Owned by pqn_owner, whose SELECT on
-- the exposed columns is what lets the planner accept a statement over base tables while the
-- caller has no access to them. A cursor accepts exactly one statement, so stacked or
-- non-plannable text is refused by PostgreSQL itself. Statements taken from pg_stat_statements
-- carry $n placeholders, which need GENERIC_PLAN (PostgreSQL 16 or later).
CREATE FUNCTION pqn_api.plan(l_query text) RETURNS jsonb
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $f$
DECLARE
  c refcursor;
  p json;
  opts text := 'FORMAT JSON';
BEGIN
  SET TRANSACTION READ ONLY;
  IF l_query ~ '\$[0-9]' THEN
    opts := 'GENERIC_PLAN, FORMAT JSON';
  END IF;
  OPEN c FOR EXECUTE format('EXPLAIN (%s) %s', opts, l_query);
  FETCH c INTO p;
  CLOSE c;
  RETURN p::jsonb;
END
$f$;

-- Run analyst SQL. Owned by pqn_reader, which can SELECT from schema pqn only. The cursor
-- refuses anything but one statement, the transaction is read only, and the row count is
-- capped. The timeout comes from the caller's own login role (see pqn_api.enroll).
CREATE FUNCTION pqn_api.run(l_query text, row_limit integer DEFAULT 100) RETURNS jsonb
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $f$
DECLARE
  c refcursor;
  r record;
  acc jsonb[] := '{}';
  n integer := 0;
  lim integer := least(greatest(coalesce(row_limit, 100), 1), 10000);
BEGIN
  SET TRANSACTION READ ONLY;
  OPEN c FOR EXECUTE l_query;
  LOOP
    FETCH c INTO r;
    EXIT WHEN NOT FOUND;
    n := n + 1;
    IF n > lim THEN
      CLOSE c;
      RETURN jsonb_build_object('rows', to_jsonb(acc), 'truncated', true);
    END IF;
    acc := acc || to_jsonb(r);
  END LOOP;
  CLOSE c;
  RETURN jsonb_build_object('rows', to_jsonb(acc), 'truncated', false);
END
$f$;

-- Statements ranked by total time, for this database. Owned by pqn_stats (member of
-- pg_monitor), the only role here that may read other users' statement text. On a standby
-- this shows the standby's own statements: read it on the primary.
CREATE FUNCTION pqn_api.top(n integer DEFAULT 20)
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
      ORDER BY s.total_exec_time DESC
      LIMIT $1', ns)
    USING least(greatest(coalesce(n, 20), 1), 500);
END
$f$;

-- ---------------------------------------------------------------------------------------
-- Recording: the ledger. Owned by pqn_ledger. Every row carries who = session_user, which
-- SET ROLE cannot change. Reading returns your own rows; pqn_admin members see all.
-- These write, so they fail on a standby, and they cannot share a transaction with pqn_api.run.
-- ---------------------------------------------------------------------------------------

CREATE FUNCTION pqn_api.record_investigation(l_query text, l_queryid bigint DEFAULT NULL,
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
      USING HINT = 'pqn_api.run and pqn_api.plan make the rest of their transaction read only. Call record_* as its own statement, or in a new transaction.';
  END IF;
  IF l_query IS NULL OR btrim(l_query) = '' THEN
    RAISE EXCEPTION 'pqn: the query text is required';
  END IF;
  IF length(l_query) > 100000 THEN
    RAISE EXCEPTION 'pqn: the query text is longer than 100000 characters';
  END IF;
  INSERT INTO pqn_ledger.investigations (sql, queryid, title)
  VALUES (l_query, l_queryid, left(l_title, 200))
  RETURNING id INTO new_id;
  RETURN new_id;
END
$f$;

CREATE FUNCTION pqn_api.record_evidence(l_investigation bigint, l_kind text, l_payload jsonb) RETURNS bigint
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
      USING HINT = 'pqn_api.run and pqn_api.plan make the rest of their transaction read only. Call record_* as its own statement, or in a new transaction.';
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
  INSERT INTO pqn_ledger.evidence (investigation_id, kind, payload)
  VALUES (l_investigation, l_kind, l_payload)
  RETURNING id INTO new_id;
  RETURN new_id;
END
$f$;

CREATE FUNCTION pqn_api.investigations(l_limit integer DEFAULT 50)
RETURNS TABLE (id bigint, who text, title text, queryid bigint, sql text, created_at timestamptz,
               evidence_count bigint)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $f$
#variable_conflict use_column
BEGIN
  RETURN QUERY
  SELECT i.id, i.who, i.title, i.queryid, i.sql, i.created_at,
         (SELECT count(*) FROM pqn_ledger.evidence e WHERE e.investigation_id = i.id)
    FROM pqn_ledger.investigations i
   WHERE i.who = session_user OR pg_has_role(session_user, 'pqn_admin', 'MEMBER')
   ORDER BY i.id DESC
   LIMIT least(greatest(coalesce(l_limit, 50), 1), 500);
END
$f$;

CREATE FUNCTION pqn_api.evidence(l_investigation bigint)
RETURNS TABLE (id bigint, kind text, payload jsonb, who text, created_at timestamptz)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $f$
#variable_conflict use_column
BEGIN
  RETURN QUERY
  SELECT e.id, e.kind, e.payload, e.who, e.created_at
    FROM pqn_ledger.evidence e
    JOIN pqn_ledger.investigations i ON i.id = e.investigation_id
   WHERE e.investigation_id = l_investigation
     AND (i.who = session_user OR pg_has_role(session_user, 'pqn_admin', 'MEMBER'))
   ORDER BY e.id;
END
$f$;

-- ---------------------------------------------------------------------------------------
-- Administration. These run with the CALLER's rights (not definer), so they work only for a
-- DBA or the installer. That is deliberate: they change privileges.
-- ---------------------------------------------------------------------------------------

-- Create the curated schema and the ledger. Idempotent. Run by the installer (or a superuser).
-- The ledger is created as pqn_ledger so the installer never owns it, and it is deliberately
-- NOT a member of the extension, so DROP EXTENSION keeps the evidence.
CREATE FUNCTION pqn_api.init() RETURNS text
LANGUAGE plpgsql AS $f$
DECLARE
  saved name := current_user;
  have int;
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

  EXECUTE format('SET LOCAL ROLE %I', saved);
  RETURN format('ledger at version %s', have);
END
$f$;

-- The statements that expose some columns of one table to analysts, for review. Column-level
-- SELECT means pqn_owner can read only what you list, so a view cannot reach a hidden column
-- even if someone writes one by hand.
CREATE FUNCTION pqn_api.expose_sql(rel regclass, cols text[], view_name text DEFAULT NULL) RETURNS text
LANGUAGE plpgsql AS $f$
DECLARE
  nsp name;
  relname name;
  kind "char";
  v name;
  collist text;
  missing text;
BEGIN
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
  RETURN array_to_string(ARRAY[
    format('GRANT USAGE ON SCHEMA %I TO pqn_owner;', nsp),
    format('GRANT SELECT (%s) ON %I.%I TO pqn_owner;', collist, nsp, relname),
    format('CREATE VIEW pqn.%I AS SELECT %s FROM %I.%I;  -- as pqn_owner', v, collist, nsp, relname),
    format('GRANT SELECT ON pqn.%I TO pqn_reader;', v)], E'\n');
END
$f$;

-- Run the statements above. Needs the right to grant on the table, so a DBA or the table owner.
CREATE FUNCTION pqn_api.expose(rel regclass, cols text[], view_name text DEFAULT NULL) RETURNS text
LANGUAGE plpgsql AS $f$
DECLARE
  script text := pqn_api.expose_sql(rel, cols, view_name);
  line text;
  saved name := current_user;
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = 'pqn') THEN
    RAISE EXCEPTION 'pqn: run pqn_api.init() first';
  END IF;
  FOREACH line IN ARRAY string_to_array(script, E'\n') LOOP
    IF line LIKE 'CREATE VIEW%' THEN
      EXECUTE 'SET LOCAL ROLE pqn_owner';
      EXECUTE regexp_replace(line, '\s*--.*$', '');
      EXECUTE format('SET LOCAL ROLE %I', saved);
    ELSE
      EXECUTE line;
    END IF;
  END LOOP;
  RETURN script;
END
$f$;

-- The statements that put one login into a group with limits. A limit set on a group role
-- does nothing, so the limits are set on the login role itself.
CREATE FUNCTION pqn_api.enroll_sql(login name, grp text DEFAULT 'analyst', stmt_timeout text DEFAULT '15s') RETURNS text
LANGUAGE plpgsql AS $f$
DECLARE
  is_super boolean;
  script text;
BEGIN
  IF grp NOT IN ('viewer', 'analyst', 'admin') THEN
    RAISE EXCEPTION 'pqn: group must be viewer, analyst or admin';
  END IF;
  IF stmt_timeout !~ '^[1-9][0-9]*(ms|s|min)?$' THEN
    RAISE EXCEPTION 'pqn: statement timeout must look like 15s, 500ms or 2min';
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
    format('ALTER ROLE %I SET idle_in_transaction_session_timeout = %L;', login, '10s')], E'\n');
  SELECT rolsuper INTO is_super FROM pg_roles WHERE rolname = current_user;
  IF is_super THEN
    script := script || E'\n' || format('ALTER ROLE %I SET temp_file_limit = %L;', login, '1GB');
  ELSE
    script := script || E'\n' || format('-- superuser only: ALTER ROLE %I SET temp_file_limit = %L;', login, '1GB');
  END IF;
  RETURN script;
END
$f$;

CREATE FUNCTION pqn_api.enroll(login name, grp text DEFAULT 'analyst', stmt_timeout text DEFAULT '15s') RETURNS text
LANGUAGE plpgsql AS $f$
DECLARE
  script text := pqn_api.enroll_sql(login, grp, stmt_timeout);
  line text;
BEGIN
  FOREACH line IN ARRAY string_to_array(script, E'\n') LOOP
    IF line NOT LIKE '--%' THEN
      EXECUTE line;
    END IF;
  END LOOP;
  RETURN script;
END
$f$;

-- ---------------------------------------------------------------------------------------
-- verify_setup(): the safety proof. Reads catalogs only, so it works on a standby.
-- One row per check. Level is BLOCK (fix before use), WARN, INFO or OK.
-- ---------------------------------------------------------------------------------------
CREATE FUNCTION pqn_api.verify_setup()
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
                        ('investigations', 'pqn_ledger'), ('evidence', 'pqn_ledger')) e(fname, expected)
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
  IF current_setting('server_version_num')::integer < 160000 THEN
    level := 'WARN'; check_name := 'server version'; detail := 'older than PostgreSQL 16: pqn_api.plan() cannot plan statements with $n placeholders';
    fix := 'upgrade to PostgreSQL 16 or later'; RETURN NEXT;
  END IF;
  IF pg_is_in_recovery() THEN
    level := 'INFO'; check_name := 'standby'; detail := 'this server is a hot standby: reading works, pqn_api.record_* and pqn_api.top() reflect this server only';
    fix := NULL; RETURN NEXT;
  END IF;
END
$f$;

-- ---------------------------------------------------------------------------------------
-- Hand each function to its narrowest owner, then close the door behind us.
-- ---------------------------------------------------------------------------------------
ALTER FUNCTION pqn_api.plan(text)                                   OWNER TO pqn_owner;
ALTER FUNCTION pqn_api.run(text, integer)                           OWNER TO pqn_reader;
ALTER FUNCTION pqn_api.top(integer)                                 OWNER TO pqn_stats;
ALTER FUNCTION pqn_api.record_investigation(text, bigint, text)     OWNER TO pqn_ledger;
ALTER FUNCTION pqn_api.record_evidence(bigint, text, jsonb)         OWNER TO pqn_ledger;
ALTER FUNCTION pqn_api.investigations(integer)                      OWNER TO pqn_ledger;
ALTER FUNCTION pqn_api.evidence(bigint)                             OWNER TO pqn_ledger;

REVOKE CREATE ON SCHEMA pqn_api FROM pqn_owner, pqn_reader, pqn_stats, pqn_ledger;

ALTER DEFAULT PRIVILEGES IN SCHEMA pqn_api REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA pqn_api FROM PUBLIC;

GRANT USAGE ON SCHEMA pqn_api TO pqn_viewer, pqn_analyst, pqn_admin;

-- Groups inherit upward (admin includes analyst includes viewer), so grant each level once.
GRANT EXECUTE ON FUNCTION pqn_api.investigations(integer), pqn_api.evidence(bigint) TO pqn_viewer;
GRANT EXECUTE ON FUNCTION pqn_api.plan(text), pqn_api.run(text, integer), pqn_api.top(integer),
                          pqn_api.record_investigation(text, bigint, text),
                          pqn_api.record_evidence(bigint, text, jsonb) TO pqn_analyst;
GRANT EXECUTE ON FUNCTION pqn_api.init(), pqn_api.verify_setup(),
                          pqn_api.expose(regclass, text[], text), pqn_api.expose_sql(regclass, text[], text),
                          pqn_api.enroll(name, text, text), pqn_api.enroll_sql(name, text, text) TO pqn_admin;
