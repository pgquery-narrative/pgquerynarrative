#!/usr/bin/env sh
# Verify the "pqn" extension in infra/pqn-extension against throwaway PostgreSQL containers.
#
# Starts its own containers and network (never touches the docker-compose stack), installs the
# extension as an ordinary non-superuser, and checks that:
#   - the install hands every SECURITY DEFINER function to its narrow role, and PUBLIC holds nothing
#   - an analyst reads only the curated views, cannot stack or write, cannot touch the ledger,
#     and cannot read a column the DBA left out
#   - the ledger records session_user, is per-user, and survives DROP EXTENSION
#   - verify_setup() reports a login with no statement_timeout, and is clean after enroll()
#   - on a hot standby, plan / run / read work and every ledger write fails cleanly
#   - the pitch, inside the database: findings from a plan, a rewrite measured against the original
#     (same rows, then faster), a wrong rewrite reported as Different, and everything recorded
#   - a real 1.0 -> 1.1 upgrade keeps the ledger and the views
#
# Requires: docker. Usage: sh tools/db/verify-pqn-extension.sh   (PG_IMAGE=postgres:16 by default)
set -eu

ROOT_DIR="$(cd "$(dirname "$0")/../.." && pwd)"
EXT_DIR="$ROOT_DIR/infra/pqn-extension"
PG_IMAGE="${PG_IMAGE:-postgres:16}"
ID="$$"
C="pqn-verify-primary-$ID"
S="pqn-verify-standby-$ID"
NET="pqn-verify-net-$ID"
VOL="pqn-verify-standby-data-$ID"
DB=appdb
PASS=0
FAIL=0

cleanup() {
  docker rm -f "$C" "$S" >/dev/null 2>&1 || true
  docker network rm "$NET" >/dev/null 2>&1 || true
  docker volume rm "$VOL" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

ok()  { PASS=$((PASS + 1)); echo "  PASS  $1"; }
bad() { FAIL=$((FAIL + 1)); echo "  FAIL  $1"; }

# run ROLE DB psql-args...   (primary)      runs ROLE DB psql-args...   (standby)
run()  { role="$1"; db="$2"; shift 2; docker exec -i "$C" psql -X -q -At -v ON_ERROR_STOP=1 -U "$role" -d "$db" "$@"; }
runs() { role="$1"; db="$2"; shift 2; docker exec -i "$S" psql -X -q -At -v ON_ERROR_STOP=1 -U "$role" -d "$db" "$@"; }
su() { run postgres "$DB" "$@"; }

expect_eq() { if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 -> got '$3', wanted '$2'"; fi; }
# expect_ok LABEL ROLE SQL
expect_ok() { if out="$(run "$2" "$DB" -c "$3" 2>&1)"; then ok "$1"; else bad "$1 -> $out"; fi; }
# expect_err LABEL PATTERN ROLE SQL   (must fail, output must match the pattern)
expect_err() {
  if out="$(run "$3" "$DB" -c "$4" 2>&1)"; then bad "$1 -> succeeded, expected an error matching '$2'"
  elif echo "$out" | grep -qiE "$2"; then ok "$1"; else bad "$1 -> $out"; fi
}
# same on the standby
expect_err_s() {
  if out="$(runs "$3" "$DB" -c "$4" 2>&1)"; then bad "$1 -> succeeded, expected an error matching '$2'"
  elif echo "$out" | grep -qiE "$2"; then ok "$1"; else bad "$1 -> $out"; fi
}
count() { run "$1" "$DB" -c "$2"; }

echo "== Starting a throwaway primary ($PG_IMAGE)"
docker network create "$NET" >/dev/null
docker run -d --name "$C" --network "$NET" -e POSTGRES_PASSWORD=x "$PG_IMAGE" \
  -c shared_preload_libraries=pg_stat_statements -c hot_standby=on -c max_wal_senders=5 >/dev/null
i=0
until docker exec "$C" psql -U postgres -Atc 'SELECT 1' >/dev/null 2>&1; do
  i=$((i + 1)); [ "$i" -gt 60 ] && { echo "PostgreSQL did not start" >&2; exit 1; }
  sleep 1
done
sleep 2
docker exec "$C" psql -X -q -U postgres -c "CREATE DATABASE $DB"

SHARE="$(docker exec "$C" pg_config --sharedir)/extension"
docker cp "$EXT_DIR/pqn.control" "$C:$SHARE/"
docker cp "$EXT_DIR/pqn--1.0.sql" "$C:$SHARE/"
docker cp "$EXT_DIR/pqn--1.0--1.1.sql" "$C:$SHARE/"
docker cp "$EXT_DIR/pqn-roles.sql" "$C:/tmp/pqn-roles.sql"

su -c "CREATE EXTENSION pg_stat_statements"
su -c "CREATE ROLE pqn_installer LOGIN"
su -c "CREATE ROLE stranger LOGIN"
su -c "CREATE ROLE parked NOLOGIN"
su -c "GRANT CREATE ON DATABASE $DB TO stranger"
for u in alice bob carol dave eve; do su -c "CREATE ROLE $u LOGIN"; done
su <<'EOF'
CREATE SCHEMA hr;
CREATE TABLE hr.people (id int PRIMARY KEY, dept text, ssn text);
INSERT INTO hr.people VALUES (1, 'sales', '111-11-1111'), (2, 'ops', '222-22-2222');
CREATE TABLE hr.events AS
  SELECT g AS id, (g % 500) AS customer, timestamptz '2025-01-01' + (g % 365) * interval '1 day' + (g % 86400) * interval '1 second' AS ts,
         'secret ' || g AS secret
    FROM generate_series(1, 600000) g;
ALTER TABLE hr.events ADD PRIMARY KEY (id);
CREATE INDEX events_ts_idx ON hr.events (ts);
ANALYZE hr.events;
EOF

echo "== Install as an ordinary role"
expect_err "an ordinary role cannot create the roles itself, and is told what to do" "Run pqn-roles.sql first, or create the extension as a superuser" stranger "CREATE EXTENSION pqn"
docker exec "$C" psql -X -q -U postgres -d "$DB" -v installer=pqn_installer -f /tmp/pqn-roles.sql
docker exec "$C" psql -X -q -U postgres -d "$DB" -v installer=pqn_installer -f /tmp/pqn-roles.sql \
  && ok "the role script is idempotent" || bad "the role script failed on a second run"
expect_err "an ordinary role that is not a member of the owner roles is refused" "must be a member" stranger "CREATE EXTENSION pqn"
expect_ok "a non-superuser installer can CREATE EXTENSION pqn" pqn_installer "CREATE EXTENSION pqn"
expect_eq "init() creates the ledger" "ledger at version 2, exposure registry ready" "$(run pqn_installer "$DB" -c "SELECT pqn_api.init()")"
expect_eq "init() is idempotent" "ledger at version 2, exposure registry ready" "$(run pqn_installer "$DB" -c "SELECT pqn_api.init()")"

echo "== Ownership and PUBLIC"
expect_eq "definer functions are owned by their narrow roles" \
  "evidence:pqn_ledger explain_ms:pqn_owner exposed:pqn_owner exposed_path:pqn_owner investigate:pqn_ledger investigations:pqn_ledger measure_pair:pqn_owner plan:pqn_owner prove:pqn_ledger record_evidence:pqn_ledger record_investigation:pqn_ledger run:pqn_reader top:pqn_stats" \
  "$(su -c "SELECT string_agg(proname || ':' || pg_get_userbyid(proowner), ' ' ORDER BY proname) FROM pg_proc WHERE pronamespace = 'pqn_api'::regnamespace AND prosecdef")"
expect_eq "no definer function is owned by a superuser or by the installer" "0" \
  "$(su -c "SELECT count(*) FROM pg_proc p JOIN pg_roles r ON r.oid = p.proowner WHERE p.pronamespace = 'pqn_api'::regnamespace AND p.prosecdef AND (r.rolsuper OR r.rolname = 'pqn_installer')")"
expect_eq "every definer function pins search_path" "0" \
  "$(su -c "SELECT count(*) FROM pg_proc p WHERE p.pronamespace = 'pqn_api'::regnamespace AND p.prosecdef AND NOT EXISTS (SELECT 1 FROM unnest(p.proconfig) c WHERE c LIKE 'search_path=%')")"
expect_eq "ledger tables and schema are owned by pqn_ledger, not the installer" "pqn_ledger" \
  "$(su -c "SELECT string_agg(DISTINCT o, ',') FROM (SELECT pg_get_userbyid(relowner) o FROM pg_class WHERE relnamespace = 'pqn_ledger'::regnamespace AND relkind IN ('r','S','i') UNION SELECT pg_get_userbyid(nspowner) FROM pg_namespace WHERE nspname = 'pqn_ledger') t")"
expect_eq "schema pqn is owned by pqn_owner" "pqn_owner" "$(su -c "SELECT pg_get_userbyid(nspowner) FROM pg_namespace WHERE nspname = 'pqn'")"
expect_eq "PUBLIC executes nothing in pqn_api" "0" \
  "$(su -c "SELECT count(*) FROM pg_proc p, LATERAL aclexplode(COALESCE(p.proacl, acldefault('f', p.proowner))) a WHERE p.pronamespace = 'pqn_api'::regnamespace AND a.grantee = 0")"

echo "== Exposing a table"
expect_err "expose_sql refuses a column that does not exist" "no column" postgres "SELECT pqn_api.expose_sql('hr.people', ARRAY['nope'])"
expect_err "expose_sql refuses the pqn schemas" "cannot be exposed" postgres "SELECT pqn_api.expose_sql('pqn_ledger.investigations', ARRAY['id'])"
expect_eq "expose_sql prints reviewable SQL" "5" "$(su -c "SELECT array_length(string_to_array(pqn_api.expose_sql('hr.people', ARRAY['id','dept']), E'\n'), 1)")"
su -c "SELECT pqn_api.expose('hr.people', ARRAY['id','dept'])" >/dev/null && ok "expose() builds the curated view" || bad "expose() failed"
expect_err "exposing the same name twice is refused" "already exists" postgres "SELECT pqn_api.expose('hr.people', ARRAY['id'])"
expect_eq "the view is owned by pqn_owner" "pqn_owner" "$(su -c "SELECT pg_get_userbyid(relowner) FROM pg_class WHERE oid = 'pqn.people'::regclass")"
expect_eq "pqn_owner can read only the listed columns" "id,dept" \
  "$(su -c "SELECT string_agg(a.attname, ',' ORDER BY a.attnum) FROM pg_attribute a WHERE a.attrelid = 'hr.people'::regclass AND a.attnum > 0 AND has_column_privilege('pqn_owner', a.attrelid, a.attnum, 'SELECT')")"

echo "== verify_setup() and enroll()"
su -c "GRANT pqn_analyst TO alice"
expect_eq "a member with no statement_timeout is a BLOCK" "alice" \
  "$(su -c "SELECT string_agg(substr(detail, 1, strpos(detail, ' ') - 1), ',') FROM pqn_api.verify_setup() WHERE level = 'BLOCK' AND check_name = 'login has no statement_timeout'")"
expect_err "enroll refuses a role that cannot log in" "not a role that can log in" postgres "SELECT pqn_api.enroll('parked')"
expect_err "enroll refuses a pqn role" "pqn role" postgres "SELECT pqn_api.enroll('pqn_reader')"
expect_err "enroll refuses a bad timeout" "statement timeout must look like" postgres "SELECT pqn_api.enroll('alice', 'analyst', '15 seconds; DROP')"
expect_err "enroll refuses an unknown group" "group must be" postgres "SELECT pqn_api.enroll('alice', 'root')"
su -c "SELECT pqn_api.enroll('alice', 'analyst')" >/dev/null
su -c "SELECT pqn_api.enroll('bob', 'viewer', '5s')" >/dev/null
su -c "SELECT pqn_api.enroll('carol', 'admin', '1min')" >/dev/null
su -c "SELECT pqn_api.enroll('dave', 'analyst')" >/dev/null
expect_eq "no BLOCK remains after enroll()" "0" "$(su -c "SELECT count(*) FROM pqn_api.verify_setup() WHERE level = 'BLOCK'")"
expect_eq "enroll() set the timeout on the login role itself" "statement_timeout=5s" \
  "$(su -c "SELECT c FROM pg_db_role_setting s, unnest(s.setconfig) c WHERE s.setrole = 'bob'::regrole AND c LIKE 'statement_timeout=%'")"
expect_eq "verify_setup() runs for a pqn_admin member" "0" "$(count carol "SELECT count(*) FROM pqn_api.verify_setup() WHERE level = 'BLOCK'")"

echo "== verify_setup() catches a bad setup"
su -c "GRANT SELECT ON hr.people TO pqn_reader"
expect_eq "a reader that can see a base table is a BLOCK" "1" "$(su -c "SELECT count(*) FROM pqn_api.verify_setup() WHERE level = 'BLOCK' AND check_name = 'reader reach'")"
su -c "REVOKE SELECT ON hr.people FROM pqn_reader"
su -c "GRANT EXECUTE ON FUNCTION pqn_api.run(text, integer) TO PUBLIC"
expect_eq "PUBLIC execute on a function is a BLOCK" "1" "$(su -c "SELECT count(*) FROM pqn_api.verify_setup() WHERE level = 'BLOCK' AND check_name = 'PUBLIC execute'")"
su -c "REVOKE ALL ON FUNCTION pqn_api.run(text, integer) FROM PUBLIC"
su -c "ALTER FUNCTION pqn_api.run(text, integer) OWNER TO postgres"
expect_eq "a definer function owned by a superuser is a BLOCK" "1" "$(su -c "SELECT count(*) FROM pqn_api.verify_setup() WHERE level = 'BLOCK' AND check_name = 'definer function ownership'")"
su -c "ALTER FUNCTION pqn_api.run(text, integer) OWNER TO pqn_reader"
su -c "SET ROLE pqn_owner; CREATE VIEW pqn.leak AS SELECT id AS ssn FROM hr.people; RESET ROLE"
expect_eq "a sensitive-looking column name in a view is a WARN" "1" "$(su -c "SELECT count(*) FROM pqn_api.verify_setup() WHERE level = 'WARN' AND check_name = 'sensitive-looking column exposed'")"
su -c "DROP VIEW pqn.leak"
expect_eq "everything is clean again" "0" "$(su -c "SELECT count(*) FROM pqn_api.verify_setup() WHERE level = 'BLOCK'")"

echo "== Who can call what"
expect_err "a role in no group cannot call the API" "permission denied" eve "SELECT pqn_api.run('SELECT 1')"
expect_err "a role in no group cannot even list investigations" "permission denied" eve "SELECT * FROM pqn_api.investigations()"
expect_ok "a viewer can list investigations" bob "SELECT * FROM pqn_api.investigations()"
expect_err "a viewer cannot run SQL" "permission denied" bob "SELECT pqn_api.run('SELECT 1')"
expect_err "a viewer cannot record" "permission denied" bob "SELECT pqn_api.record_investigation('SELECT 1')"
expect_err "an analyst cannot call verify_setup" "permission denied" alice "SELECT * FROM pqn_api.verify_setup()"
expect_err "an analyst cannot expose a table" "permission denied" alice "SELECT pqn_api.expose('hr.people', ARRAY['id'], 'p2')"
expect_err "an analyst cannot enroll themselves into admin" "permission denied" alice "SELECT pqn_api.enroll('alice', 'admin')"

echo "== run(): analyst SQL"
expect_eq "run returns the curated rows as JSON" "2" "$(count alice "SELECT jsonb_array_length(pqn_api.run('SELECT * FROM pqn.people')->'rows')")"
expect_eq "the rows carry only the exposed columns" "dept,id" \
  "$(count alice "SELECT string_agg(k, ',' ORDER BY k) FROM jsonb_object_keys((pqn_api.run('SELECT * FROM pqn.people')->'rows')->0) k")"
expect_eq "the row cap truncates and says so" "5 true" \
  "$(count alice "SELECT jsonb_array_length(r->'rows') || ' ' || (r->>'truncated') FROM (SELECT pqn_api.run('SELECT g FROM generate_series(1, 500) g', 5) r) x")"
expect_eq "a limit of 0 is clamped to 1" "1" "$(count alice "SELECT jsonb_array_length(pqn_api.run('SELECT g FROM generate_series(1, 5) g', 0)->'rows')")"
expect_eq "an empty result is an empty list" "0 false" \
  "$(count alice "SELECT jsonb_array_length(r->'rows') || ' ' || (r->>'truncated') FROM (SELECT pqn_api.run('SELECT id FROM pqn.people WHERE false') r) x")"
expect_err "a base table is refused" "permission denied" alice "SELECT pqn_api.run('SELECT * FROM hr.people')"
expect_err "a hidden column is refused" "permission denied" alice "SELECT pqn_api.run('SELECT ssn FROM hr.people')"
expect_err "the ledger is refused" "permission denied" alice "SELECT pqn_api.run('SELECT * FROM pqn_ledger.investigations')"
expect_err "the system password table is refused" "permission denied" alice "SELECT pqn_api.run('SELECT * FROM pg_authid')"
expect_err "server files are refused" "permission denied" alice "SELECT pqn_api.run('SELECT pg_read_file(''/etc/passwd'')')"
expect_err "calling the API from inside analyst SQL is refused" "permission denied" alice "SELECT pqn_api.run('SELECT pqn_api.record_investigation(''x'')')"
expect_err "stacked statements are refused" "multi-query" alice "SELECT pqn_api.run('SELECT 1; SELECT 2')"
expect_err "DDL is refused" "cannot open" alice "SELECT pqn_api.run('CREATE TABLE hr.evil (i int)')"
expect_err "DELETE is refused" "cannot open" alice "SELECT pqn_api.run('DELETE FROM pqn.people')"
expect_err "SELECT ... INTO is refused" "cannot open|permission denied|read-only" alice "SELECT pqn_api.run('SELECT id INTO hr.copy FROM pqn.people')"
expect_eq "nothing was created or deleted" "2 0" "$(su -c "SELECT (SELECT count(*) FROM hr.people) || ' ' || count(*) FROM pg_class WHERE relname IN ('evil', 'copy')")"
run alice "$DB" -c "SET statement_timeout = '300ms'; SELECT pqn_api.run('SELECT pg_sleep(3)')" >/tmp/pqn-verify-$$.out 2>&1 && bad "a slow query was not stopped" || { grep -qi "statement timeout" /tmp/pqn-verify-$$.out && ok "statement_timeout stops a slow query" || bad "slow query error: $(cat /tmp/pqn-verify-$$.out)"; }
rm -f /tmp/pqn-verify-$$.out

echo "== plan(): estimated plans without data access"
expect_eq "plan returns a JSON plan" "Seq Scan" "$(count alice "SELECT pqn_api.plan('SELECT id FROM hr.people WHERE dept = ''ops''')->0->'Plan'->>'Node Type'")"
expect_eq "plan handles \$1 placeholders (GENERIC_PLAN)" "t" "$(count alice "SELECT pqn_api.plan('SELECT id FROM pqn.people WHERE id = \$1') IS NOT NULL")"
expect_err "plan cannot see a hidden column" "permission denied" alice "SELECT pqn_api.plan('SELECT ssn FROM hr.people')"
expect_err "plan refuses stacked statements" "multi-query|syntax" alice "SELECT pqn_api.plan('SELECT 1; DROP TABLE hr.people')"
expect_err "plan does not run DDL" "syntax|cannot" alice "SELECT pqn_api.plan('DROP TABLE hr.people')"
expect_eq "the table is still there" "2" "$(su -c "SELECT count(*) FROM hr.people")"

echo "== top(): workload statistics"
expect_eq "top returns statements for this database" "t" "$(count alice "SELECT count(*) > 0 FROM pqn_api.top(5)")"
expect_eq "top honours its limit" "t" "$(count alice "SELECT count(*) <= 2 FROM pqn_api.top(2)")"

echo "== The ledger"
ID1="$(count alice "SELECT pqn_api.record_investigation('SELECT * FROM pqn.people', 123, 'slow people query')")"
expect_eq "record_investigation returns an id" "1" "$ID1"
expect_eq "the row is attributed to the caller" "alice" "$(su -c "SELECT who FROM pqn_ledger.investigations WHERE id = $ID1")"
expect_eq "who stays alice after SET ROLE to her group" "alice" \
  "$(run alice "$DB" -c "SET ROLE pqn_analyst; SELECT pqn_api.record_investigation('SELECT 2')" >/dev/null; su -c "SELECT who FROM pqn_ledger.investigations ORDER BY id DESC LIMIT 1")"
expect_ok "an analyst can add evidence to their own investigation" alice "SELECT pqn_api.record_evidence($ID1, 'plan', '{\"cost\": 12.5}')"
expect_err "evidence kind is validated" "kind must be" alice "SELECT pqn_api.record_evidence($ID1, 'DROP TABLE', '{}')"
expect_err "a viewer cannot add evidence" "permission denied" bob "SELECT pqn_api.record_evidence($ID1, 'note', '{}')"
expect_err "an analyst cannot add evidence to someone else's investigation" "no investigation" dave "SELECT pqn_api.record_evidence($ID1, 'note', '{}')"
expect_eq "an admin can add evidence to anyone's investigation" "t" "$(count carol "SELECT pqn_api.record_evidence($ID1, 'note', '{}') > 0")"
expect_err "a missing investigation is refused" "no investigation" alice "SELECT pqn_api.record_evidence(999999, 'note', '{}')"
expect_eq "evidence is read back in order" "plan,note" "$(count alice "SELECT string_agg(kind, ',' ORDER BY id) FROM pqn_api.evidence($ID1)")"
expect_eq "a viewer sees none of another user's investigations" "0" "$(count bob "SELECT count(*) FROM pqn_api.investigations()")"
expect_eq "a viewer sees none of another user's evidence" "0" "$(count bob "SELECT count(*) FROM pqn_api.evidence($ID1)")"
expect_eq "another analyst sees none of them either" "0" "$(count dave "SELECT count(*) FROM pqn_api.investigations()")"
expect_eq "an admin sees everyone's investigations" "2" "$(count carol "SELECT count(*) FROM pqn_api.investigations()")"
expect_err "an analyst cannot insert into the ledger directly" "permission denied" alice "INSERT INTO pqn_ledger.investigations(sql) VALUES ('x')"
expect_err "an analyst cannot read the ledger directly" "permission denied" alice "SELECT * FROM pqn_ledger.investigations"
expect_err "an analyst cannot rewrite history" "permission denied" alice "UPDATE pqn_ledger.evidence SET payload = '{}'"
expect_err "an analyst cannot delete history" "permission denied" alice "DELETE FROM pqn_ledger.investigations"
expect_err "an analyst cannot truncate history" "permission denied" alice "TRUNCATE pqn_ledger.evidence"
expect_err "running SQL and recording in one transaction fails (they are two calls)" "read-only transaction" alice \
  "BEGIN; SELECT pqn_api.run('SELECT 1'); SELECT pqn_api.record_investigation('SELECT 1'); COMMIT;"

echo "== 1.1: unqualified names, ordered columns, plans that compose"
expect_eq "run resolves plain names to the curated views" "2" "$(count alice "SELECT (pqn_api.run('SELECT count(*) AS n FROM people')->'rows'->0->>'n')::int")"
expect_eq "run keeps the column order" '["dept", "id"]' "$(count alice "SELECT (pqn_api.run('SELECT dept, id FROM people'))->'columns'")"
expect_err "a plain name for a table that was never exposed does not resolve" "does not exist" alice "SELECT pqn_api.run('SELECT * FROM events')"
expect_eq "plan(...) passes straight into record_evidence(...)" "t" "$(count alice "SELECT pqn_api.record_evidence($ID1, 'plan', pqn_api.plan('SELECT id FROM hr.people')) > 0")"

echo "== Exposure scopes"
expect_err "scope must be view or full" "scope must be" postgres "SELECT pqn_api.expose_sql('hr.events', ARRAY['id'], NULL, 'everything')"
su -c "SELECT pqn_api.expose('hr.events', ARRAY['id','customer','ts'], 'events', 'view')" >/dev/null
expect_err "scope view: SELECT * over a table with a hidden column cannot be planned" "permission denied" alice "SELECT pqn_api.plan('SELECT * FROM hr.events')"
expect_eq "scope view: a statement on exposed columns can" "t" "$(count alice "SELECT pqn_api.plan('SELECT id FROM hr.events WHERE customer = 7') IS NOT NULL")"
HIDDEN_A="SELECT id FROM hr.events WHERE secret = 'secret 5'"
HIDDEN_B="SELECT id FROM hr.events WHERE id = 5"
expect_err "scope view: a hidden column cannot be probed through row counts" "permission denied" alice "SELECT pqn_api.measure_pair(\$q\$$HIDDEN_A\$q\$, \$q\$$HIDDEN_B\$q\$)"
expect_eq "the registry lists the exposure" "hr|events|events|view" "$(su -c "SELECT schema_name || '|' || table_name || '|' || view_name || '|' || scope FROM pqn_api.exposed() WHERE view_name = 'events'")"
su -c "SELECT pqn_api.unexpose('events')" >/dev/null
su -c "SELECT pqn_api.expose('hr.events', ARRAY['id','customer','ts'], 'events', 'full')" >/dev/null
expect_eq "scope full: SELECT * can be planned" "t" "$(count alice "SELECT pqn_api.plan('SELECT * FROM hr.events WHERE customer = 7') IS NOT NULL")"
expect_eq "scope full: run still returns only the exposed columns" '["customer", "id", "ts"]' \
  "$(count alice "SELECT (SELECT jsonb_agg(k ORDER BY k) FROM jsonb_object_keys(pqn_api.run('SELECT * FROM events LIMIT 1')->'rows'->0) k)")"
expect_err "scope full: run still cannot read the hidden column" "does not exist|permission denied" alice "SELECT pqn_api.run('SELECT secret FROM events')"
expect_eq "scope full is a WARN in verify_setup" "1" "$(su -c "SELECT count(*) FROM pqn_api.verify_setup() WHERE level = 'WARN' AND check_name = 'table exposed with scope full'")"
expect_eq "unexpose(): the view is gone" "t" "$(su -c "SELECT pqn_api.unexpose('people') IS NOT NULL AND to_regclass('pqn.people') IS NULL")"
expect_eq "unexpose(): pqn_owner lost the table" "f" "$(su -c "SELECT has_any_column_privilege('pqn_owner', 'hr.people'::regclass, 'SELECT')")"
su -c "SELECT pqn_api.expose('hr.people', ARRAY['id','dept'])" >/dev/null

echo "== findings(): rules over a plan"
FIND="$(count alice "SELECT string_agg(DISTINCT f->>'category', ',' ORDER BY f->>'category') FROM jsonb_array_elements(pqn_api.findings(pqn_api.plan('SELECT id FROM hr.events WHERE date_trunc(''day'', ts) = ''2025-03-01'''))) f")"
expect_eq "a function on an indexed column is a function_on_column finding" "function_on_column,seq_scan" "$FIND"
expect_eq "an unindexed equality gets an index proposal" "CREATE INDEX CONCURRENTLY ON hr.events (customer)" \
  "$(count alice "SELECT f->>'advice_ddl' FROM jsonb_array_elements(pqn_api.findings(pqn_api.plan('SELECT id FROM hr.events WHERE customer = 7'))) f WHERE f->>'category' = 'index_candidate'")"
expect_eq "an indexed column gets no index proposal" "0" \
  "$(count alice "SELECT count(*) FROM jsonb_array_elements(pqn_api.findings(pqn_api.plan('SELECT id FROM hr.events WHERE ts >= ''2025-03-01'' AND ts < ''2025-03-02'''))) f WHERE f->>'category' = 'index_candidate'")"

echo "== measure_pair(): same rows, then faster"
DT="SELECT id, customer FROM hr.events WHERE date_trunc('day', ts) = '2025-03-01'"
RG="SELECT id, customer FROM hr.events WHERE ts >= '2025-03-01' AND ts < '2025-03-02'"
WRONG="SELECT id, customer FROM hr.events WHERE ts >= '2025-03-01' AND ts < '2025-03-03'"
expect_eq "equal rows are detected" "true" "$(count alice "SELECT pqn_api.measure_pair(\$q\$$DT\$q\$, \$q\$$RG\$q\$)->>'equal'")"
expect_eq "the rewrite is measurably faster" "t" "$(count alice "SELECT (pqn_api.measure_pair(\$q\$$DT\$q\$, \$q\$$RG\$q\$)->>'speedup')::numeric >= 1.2")"
expect_eq "the row count is the same on both sides" "t" "$(count alice "SELECT m->'before'->>'rows' = m->'after'->>'rows' FROM (SELECT pqn_api.measure_pair(\$q\$$DT\$q\$, \$q\$$RG\$q\$) m) x")"
expect_eq "different rows are detected" "false" "$(count alice "SELECT pqn_api.measure_pair(\$q\$$DT\$q\$, \$q\$$WRONG\$q\$)->>'equal'")"
expect_eq "row order does not matter" "true" "$(count alice "SELECT pqn_api.measure_pair('SELECT id FROM hr.events WHERE id < 50 ORDER BY id', 'SELECT id FROM hr.events WHERE id < 50 ORDER BY id DESC')->>'equal'")"
expect_err "placeholders cannot be executed" "placeholders" alice "SELECT pqn_api.measure_pair('SELECT id FROM hr.events WHERE id = \$1', 'SELECT 1')"
expect_err "a statement that closes the wrapper and continues is refused" "syntax|multi-query|cannot open" alice "SELECT pqn_api.measure_pair('SELECT 1) t; DELETE FROM hr.events; SELECT count(*) FROM (SELECT 1', 'SELECT 1')"
expect_err "DML is refused" "cannot open|syntax" alice "SELECT pqn_api.measure_pair('DELETE FROM hr.events', 'SELECT 1')"
expect_eq "nothing was deleted" "600000" "$(su -c "SELECT count(*) FROM hr.events")"
expect_err "the timing helper cannot be called directly, it would run any statement" "permission denied" alice "SELECT pqn_api.explain_ms('SELECT 1', 'pg_catalog')"
expect_eq "the timing comes from EXPLAIN ANALYZE and is a positive number" "t" "$(count alice "SELECT (pqn_api.measure_pair(\$q\$$DT\$q\$, \$q\$$RG\$q\$)->'before'->>'ms')::numeric > 0")"
expect_err "a viewer cannot measure" "permission denied" bob "SELECT pqn_api.measure_pair('SELECT 1', 'SELECT 1')"

echo "== investigate() and prove(): propose, then prove, and record it"
INV="$(count alice "SELECT pqn_api.investigate(\$q\$$DT\$q\$, 'day filter', 99)->>'investigation_id'")"
expect_eq "investigate records an investigation for the caller with the queryid" "t" \
  "$(su -c "SELECT (SELECT who FROM pqn_ledger.investigations WHERE id = $INV) = 'alice' AND (SELECT queryid FROM pqn_ledger.investigations WHERE id = $INV) = 99")"
expect_eq "investigate records the plan and the findings" "findings,plan" "$(su -c "SELECT string_agg(kind, ',' ORDER BY kind) FROM pqn_ledger.evidence WHERE investigation_id = $INV")"
expect_eq "investigate names the problem" "t" "$(count alice "SELECT pqn_api.investigate(\$q\$$DT\$q\$)->'findings' @> '[{\"category\": \"function_on_column\"}]'")"
P1="$(count alice "SELECT pqn_api.prove($INV, \$q\$$DT\$q\$, \$q\$$RG\$q\$, 'sargable range')")"
expect_eq "a correct, faster rewrite is Proven" "Proven" "$(su -c "SELECT (\$j\$$P1\$j\$::jsonb)->>'verdict'")"
expect_eq "the proof carries the cost of both plans" "t" "$(su -c "SELECT ((\$j\$$P1\$j\$::jsonb)->>'cost_after')::numeric < ((\$j\$$P1\$j\$::jsonb)->>'cost_before')::numeric")"
expect_eq "a wrong rewrite is Different, however fast" "Different" "$(count alice "SELECT pqn_api.prove($INV, \$q\$$DT\$q\$, \$q\$$WRONG\$q\$)->>'verdict'")"
expect_eq "a slower rewrite is NotFaster" "NotFaster" "$(count alice "SELECT pqn_api.prove($INV, \$q\$$RG\$q\$, \$q\$$DT\$q\$)->>'verdict'")"
expect_eq "placeholders are Unverified, and nothing is executed" "Unverified" \
  "$(count alice "SELECT pqn_api.prove($INV, 'SELECT id FROM hr.events WHERE customer = \$1', 'SELECT id FROM hr.events WHERE customer = \$1 ORDER BY id')->>'verdict'")"
expect_eq "every proof is in the ledger under the caller" "4 alice" "$(su -c "SELECT count(*) || ' ' || min(who) FROM pqn_ledger.evidence WHERE investigation_id = $INV AND kind = 'proof'")"
expect_eq "a proof computed by prove() is stamped source=database" "database" "$(su -c "SELECT DISTINCT payload->>'source' FROM pqn_ledger.evidence WHERE investigation_id = $INV AND kind = 'proof'")"
INVF="$(count alice "SELECT pqn_api.record_investigation('SELECT 1', NULL, 'forgery')")"
FORGED="$(count alice "SELECT pqn_api.record_evidence($INVF, 'proof', '{\"verdict\": \"Proven\", \"reason\": \"same rows, 9999x faster\", \"source\": \"database\"}'::jsonb)")"
expect_eq "a proof an analyst records is stamped source=client, whatever they claim" "client" "$(su -c "SELECT payload->>'source' FROM pqn_ledger.evidence WHERE id = $FORGED")"
expect_err "a proof must be an object" "must be a JSON object" alice "SELECT pqn_api.record_evidence($INVF, 'proof', '[1]'::jsonb)"
NOTEID="$(count alice "SELECT pqn_api.record_evidence($INVF, 'note', '{\"x\": 1}'::jsonb)")"
expect_eq "other evidence kinds are stored exactly as given" '{"x": 1}' "$(su -c "SELECT payload FROM pqn_ledger.evidence WHERE id = $NOTEID")"
expect_err "another analyst cannot prove into your investigation" "no investigation" dave "SELECT pqn_api.prove($INV, 'SELECT 1', 'SELECT 1')"
expect_err "a viewer cannot prove" "permission denied" bob "SELECT pqn_api.prove($INV, 'SELECT 1', 'SELECT 1')"
expect_err "investigate after run in one transaction explains itself" "own statement" alice "BEGIN; SELECT pqn_api.run('SELECT 1'); SELECT pqn_api.investigate('SELECT 1'); COMMIT;"

echo "== verify_setup() for the new surface"
su -c "GRANT CREATE ON SCHEMA hr TO alice"
expect_eq "a member who can create objects in a planning schema is a BLOCK" "1" "$(su -c "SELECT count(*) FROM pqn_api.verify_setup() WHERE level = 'BLOCK' AND check_name = 'member can create objects in a planning schema'")"
su -c "REVOKE CREATE ON SCHEMA hr FROM alice"
su -c "ALTER FUNCTION pqn_api.plan(text) SET search_path = pg_catalog, public"
expect_eq "a definer path that names another schema is a BLOCK" "1" "$(su -c "SELECT count(*) FROM pqn_api.verify_setup() WHERE level = 'BLOCK' AND check_name = 'definer search_path scope'")"
su -c "ALTER FUNCTION pqn_api.plan(text) SET search_path = pg_catalog, pg_temp"
expect_eq "everything is clean again after the 1.1 checks" "0" "$(su -c "SELECT count(*) FROM pqn_api.verify_setup() WHERE level = 'BLOCK'")"

echo "== A real upgrade: 1.0 with data, then ALTER EXTENSION UPDATE"
run postgres postgres -c "CREATE DATABASE upg" >/dev/null
sup() { run postgres upg "$@"; }
sup -c "CREATE SCHEMA hr; CREATE TABLE hr.people (id int PRIMARY KEY, dept text, ssn text); INSERT INTO hr.people VALUES (1, 'sales', 'x'), (2, 'ops', 'y')"
sup -c "CREATE EXTENSION pg_stat_statements"
sup -c "GRANT CREATE ON DATABASE upg TO pqn_installer"
run pqn_installer upg -c "CREATE EXTENSION pqn VERSION '1.0'" >/dev/null && ok "version 1.0 installs on its own" || bad "version 1.0 did not install"
expect_eq "1.0 is what is installed" "1.0" "$(sup -c "SELECT extversion FROM pg_extension WHERE extname = 'pqn'")"
run pqn_installer upg -c "SELECT pqn_api.init()" >/dev/null
expect_eq "1.0 made the ledger at version 1" "1" "$(sup -c "SELECT max(version) FROM pqn_ledger.schema_migrations")"
sup -c "SELECT pqn_api.expose('hr.people', ARRAY['id','dept'])" >/dev/null
UID1="$(run alice upg -c "SELECT pqn_api.record_investigation('SELECT 1 FROM hr.people', NULL, 'made under 1.0')")"
expect_eq "1.0: run needs the qualified name" "2" "$(run alice upg -c "SELECT jsonb_array_length(pqn_api.run('SELECT * FROM pqn.people')->'rows')")"
run pqn_installer upg -c "ALTER EXTENSION pqn UPDATE" >/dev/null && ok "ALTER EXTENSION UPDATE runs as an ordinary installer" || bad "the update failed"
run pqn_installer upg -c "SELECT pqn_api.init()" >/dev/null
expect_eq "the extension is at 1.1" "1.1" "$(sup -c "SELECT extversion FROM pg_extension WHERE extname = 'pqn'")"
expect_eq "the ledger row made under 1.0 survived" "made under 1.0" "$(sup -c "SELECT title FROM pqn_ledger.investigations WHERE id = $UID1")"
expect_eq "the 1.0 view was backfilled into the registry" "hr|people|view" "$(sup -c "SELECT schema_name || '|' || table_name || '|' || scope FROM pqn_api.exposed()")"
expect_eq "run now resolves plain names" "2" "$(run alice upg -c "SELECT jsonb_array_length(pqn_api.run('SELECT * FROM people')->'rows')")"
expect_eq "the new functions exist with the right owners" "exposed:pqn_owner exposed_path:pqn_owner investigate:pqn_ledger measure_pair:pqn_owner prove:pqn_ledger" \
  "$(sup -c "SELECT string_agg(proname || ':' || pg_get_userbyid(proowner), ' ' ORDER BY proname) FROM pg_proc WHERE pronamespace = 'pqn_api'::regnamespace AND proname IN ('exposed','exposed_path','investigate','measure_pair','prove')")"
expect_eq "the update left nothing granted to PUBLIC" "0" \
  "$(sup -c "SELECT count(*) FROM pg_proc p, LATERAL aclexplode(COALESCE(p.proacl, acldefault('f', p.proowner))) a WHERE p.pronamespace = 'pqn_api'::regnamespace AND a.grantee = 0")"
expect_eq "no BLOCK after the update" "0" "$(sup -c "SELECT count(*) FROM pqn_api.verify_setup() WHERE level = 'BLOCK'")"
expect_eq "a proof works after the update and finds the same rows" "true" \
  "$(run alice upg -c "SELECT pqn_api.prove($UID1, 'SELECT id FROM hr.people WHERE dept = ''ops''', 'SELECT id FROM hr.people WHERE dept = ''ops'' AND id > 0')->'measurement'->>'equal'")"

echo "== DROP EXTENSION keeps the evidence"
run pqn_installer "$DB" -c "DROP EXTENSION pqn"
expect_eq "the ledger rows are still there" "5" "$(su -c "SELECT count(*) FROM pqn_ledger.investigations")"
expect_eq "the curated view is still there" "t" "$(su -c "SELECT to_regclass('pqn.people') IS NOT NULL")"
expect_eq "the extension's functions are gone" "0" "$(su -c "SELECT count(*) FROM pg_proc WHERE pronamespace = 'pqn_api'::regnamespace")"
run pqn_installer "$DB" -c "CREATE EXTENSION pqn" && ok "CREATE EXTENSION works again" || bad "re-CREATE EXTENSION failed"
expect_eq "init() finds the existing ledger" "ledger at version 2, exposure registry ready" "$(run pqn_installer "$DB" -c "SELECT pqn_api.init()")"
su -c "SELECT pqn_api.enroll('alice', 'analyst')" >/dev/null
expect_eq "old evidence is readable through the new install" "plan,note,plan" "$(count alice "SELECT string_agg(kind, ',' ORDER BY id) FROM pqn_api.evidence($ID1)")"
expect_eq "nothing is granted to PUBLIC after the reinstall" "0" \
  "$(su -c "SELECT count(*) FROM pg_proc p, LATERAL aclexplode(COALESCE(p.proacl, acldefault('f', p.proowner))) a WHERE p.pronamespace = 'pqn_api'::regnamespace AND a.grantee = 0")"
expect_eq "verify_setup() is clean after the reinstall" "0" "$(su -c "SELECT count(*) FROM pqn_api.verify_setup() WHERE level = 'BLOCK'")"

echo "== Owner roles are for install time only"
expect_eq "an installer left in the owner roles is a WARN" "4" "$(su -c "SELECT count(*) FROM pqn_api.verify_setup() WHERE level = 'WARN' AND check_name = 'login can become an owner role' AND detail LIKE 'pqn_installer %'")"
su -c "REVOKE pqn_owner, pqn_reader, pqn_stats, pqn_ledger FROM pqn_installer"
expect_eq "no login can become an owner role once the installer is removed" "0" "$(su -c "SELECT count(*) FROM pqn_api.verify_setup() WHERE check_name = 'login can become an owner role'")"
expect_eq "analysts sharing the pqn_reader identity in pg_stat_statements is reported" "1" "$(su -c "SELECT count(*) FROM pqn_api.verify_setup() WHERE level = 'WARN' AND check_name = 'reader sees pg_stat_statements'")"

echo "== Hot standby"
su -c "CREATE ROLE repl LOGIN REPLICATION PASSWORD 'repl'"
docker exec "$C" sh -c 'echo "host replication repl all scram-sha-256" >> "$PGDATA/pg_hba.conf"'
su -c "SELECT pg_reload_conf()" >/dev/null
# The standby keeps its data in its own volume at a path we choose, so this works for images whose
# default data directory differs (PostgreSQL 18 moved it).
docker volume create "$VOL" >/dev/null
docker run --rm --user root -v "$VOL:/pgv" "$PG_IMAGE" \
  sh -c 'mkdir -p /pgv/data && chown postgres:postgres /pgv/data && chmod 700 /pgv/data'
if docker run --rm --network "$NET" --user postgres -e PGPASSWORD=repl -v "$VOL:/pgv" "$PG_IMAGE" \
     pg_basebackup -h "$C" -U repl -D /pgv/data -R -X stream >/dev/null 2>&1; then
  docker run -d --name "$S" --network "$NET" -e PGDATA=/pgv/data -v "$VOL:/pgv" "$PG_IMAGE" \
    -c shared_preload_libraries=pg_stat_statements -c hot_standby=on >/dev/null
  i=0
  until [ "$(runs postgres "$DB" -c "SELECT count(*) FROM pqn_ledger.investigations" 2>/dev/null || echo x)" = "2" ]; do
    i=$((i + 1)); [ "$i" -gt 60 ] && break
    sleep 1
  done
  expect_eq "the standby is in recovery" "t" "$(runs postgres "$DB" -c "SELECT pg_is_in_recovery()")"
  expect_eq "the extension objects replicated" "t" "$(runs postgres "$DB" -c "SELECT to_regprocedure('pqn_api.run(text,integer)') IS NOT NULL")"
  expect_eq "run works on the standby" "2" "$(runs alice "$DB" -c "SELECT jsonb_array_length(pqn_api.run('SELECT * FROM pqn.people')->'rows')")"
  expect_eq "plan works on the standby" "Seq Scan" "$(runs alice "$DB" -c "SELECT pqn_api.plan('SELECT id FROM hr.people WHERE dept = ''ops''')->0->'Plan'->>'Node Type'")"
  expect_eq "plan with \$1 works on the standby" "t" "$(runs alice "$DB" -c "SELECT pqn_api.plan('SELECT id FROM pqn.people WHERE id = \$1') IS NOT NULL")"
  expect_eq "the ledger is readable on the standby" "plan,note,plan" "$(runs alice "$DB" -c "SELECT string_agg(kind, ',' ORDER BY id) FROM pqn_api.evidence($ID1)")"
  expect_eq "top works on the standby (its own statements)" "t" "$(runs alice "$DB" -c "SELECT count(*) >= 0 FROM pqn_api.top(3)")"
  expect_err_s "recording fails cleanly on the standby and says to use the primary" "standby" alice "SELECT pqn_api.record_investigation('SELECT 1')"
  expect_eq "measure_pair works on the standby" "true" "$(runs alice "$DB" -c "SELECT pqn_api.measure_pair('SELECT id FROM hr.people WHERE id < 5', 'SELECT id FROM hr.people WHERE id < 5 AND true')->>'equal'")"
  expect_eq "findings work on the standby" "t" "$(runs alice "$DB" -c "SELECT jsonb_array_length(pqn_api.findings(pqn_api.plan(\$q\$SELECT id FROM hr.events WHERE date_trunc('day', ts) = '2025-03-01'\$q\$))) >= 1")"
  expect_err_s "investigate fails cleanly on the standby and says so" "standby" alice "SELECT pqn_api.investigate('SELECT 1 FROM hr.people')"
  expect_err_s "prove fails cleanly on the standby and says so" "standby" alice "SELECT pqn_api.prove(1, 'SELECT 1', 'SELECT 1')"
  expect_err_s "a hidden column is still refused on the standby" "permission denied" alice "SELECT pqn_api.run('SELECT ssn FROM hr.people')"
  expect_err_s "a base table is still refused on the standby" "permission denied" alice "SELECT pqn_api.plan('SELECT ssn FROM hr.people')"
  expect_err_s "CREATE EXTENSION is refused on the standby" "read-only transaction" pqn_installer "CREATE EXTENSION IF NOT EXISTS hstore"
  expect_eq "verify_setup() works on the standby and says so" "1 0" \
    "$(runs carol "$DB" -c "SELECT (SELECT count(*) FROM pqn_api.verify_setup() WHERE level = 'INFO' AND check_name = 'standby') || ' ' || (SELECT count(*) FROM pqn_api.verify_setup() WHERE level = 'BLOCK')")"
  expect_eq "per-login limits replicated to the standby" "statement_timeout=15s" \
    "$(runs postgres "$DB" -c "SELECT c FROM pg_db_role_setting s, unnest(s.setconfig) c WHERE s.setrole = 'alice'::regrole AND c LIKE 'statement_timeout=%'")"
else
  bad "could not take a base backup for the standby"
fi

echo
echo "RESULT pqn extension: pass=$PASS fail=$FAIL"
[ "$FAIL" -eq 0 ]
