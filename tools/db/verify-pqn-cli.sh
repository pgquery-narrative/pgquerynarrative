#!/usr/bin/env sh
# End to end: the pqn terminal tool against a throwaway PostgreSQL that has the pqn extension and a
# slow-query lab. It checks the pitch from a terminal, as a person would use it:
#
#   top -> investigate (findings, a rewrite proposed from the plan, proof) -> ledger
#   a wrong rewrite is Different (never an improvement), a correct one is Proven
#   the limits hold from the terminal: no writes, no hidden column, viewers and strangers refused
#
# Requires: docker, python3, and the pqn binary (make build-pqn). Never touches your own containers.
# Usage: sh tools/db/verify-pqn-cli.sh        (PG_IMAGE=postgres:18 by default, PQN_BIN=bin/pqn)
set -eu

ROOT_DIR="$(cd "$(dirname "$0")/../.." && pwd)"
EXT_DIR="$ROOT_DIR/infra/pqn-extension"
PQN="${PQN_BIN:-$ROOT_DIR/bin/pqn}"
PG_IMAGE="${PG_IMAGE:-postgres:18}"
C="pqn-cli-verify-$$"
DB=appdb
PASS=0
FAIL=0

[ -x "$PQN" ] || { echo "pqn binary not found at $PQN. Run: make build-pqn" >&2; exit 1; }
cleanup() { docker rm -f "$C" >/dev/null 2>&1 || true; rm -f "/tmp/pqn-cli-$$".*; }
trap cleanup EXIT INT TERM

ok()  { PASS=$((PASS + 1)); echo "  PASS  $1"; }
bad() { FAIL=$((FAIL + 1)); echo "  FAIL  $1"; }
su()  { docker exec -i "$C" psql -X -q -At -v ON_ERROR_STOP=1 -U postgres -d "$DB" "$@"; }

echo "== Starting a throwaway PostgreSQL ($PG_IMAGE) with pg_stat_statements"
docker run -d --name "$C" -p 127.0.0.1::5432 -e POSTGRES_PASSWORD=x "$PG_IMAGE" \
  -c shared_preload_libraries=pg_stat_statements >/dev/null
i=0
until docker exec "$C" psql -U postgres -Atc 'SELECT 1' >/dev/null 2>&1; do
  i=$((i + 1)); [ "$i" -gt 60 ] && { echo "PostgreSQL did not start" >&2; exit 1; }
  sleep 1
done
sleep 2
PORT="$(docker port "$C" 5432/tcp | head -n 1 | sed 's/.*://')"
docker exec "$C" psql -X -q -U postgres -c "CREATE DATABASE $DB"

SHARE="$(docker exec "$C" pg_config --sharedir)/extension"
for f in pqn.control pqn--1.0.sql pqn--1.0--1.1.sql; do docker cp "$EXT_DIR/$f" "$C:$SHARE/"; done

su -c "CREATE EXTENSION pg_stat_statements"
for u in alice bob carol eve; do su -c "CREATE ROLE $u LOGIN PASSWORD '$u-pw'"; done
# A superuser creates the extension and the roles are made for it: no role script, no installer role.
su -c "CREATE EXTENSION pqn"
su -c "SELECT pqn_api.init()" >/dev/null

su <<'EOF'
CREATE SCHEMA shop;
CREATE TABLE shop.orders AS
  SELECT g AS id, (1 + (g::bigint * 7919) % 5000)::int AS customer_id,
         timestamptz '2024-01-01' + ((g::bigint * 13) % 730)::int * interval '1 day' + (g % 86400) * interval '1 second' AS created_at,
         (ARRAY['paid','new','refunded','shipped'])[1 + g % 4] AS status,
         ((g::bigint * 31) % 9973)::numeric / 10 AS amount,
         'private note ' || g AS notes
    FROM generate_series(1, 800000) g;
ALTER TABLE shop.orders ADD PRIMARY KEY (id);
CREATE INDEX orders_created_idx ON shop.orders (created_at);
ANALYZE shop.orders;
EOF
su -c "SELECT pqn_api.expose('shop.orders', ARRAY['id','customer_id','created_at','status','amount'], 'orders', 'full')" >/dev/null
su -c "SELECT pqn_api.enroll('alice', 'analyst', '120s')" >/dev/null
su -c "SELECT pqn_api.enroll('bob', 'viewer')" >/dev/null
su -c "SELECT pqn_api.enroll('carol', 'admin')" >/dev/null

# The application's slow traffic, so pg_stat_statements has something to rank.
for d in 03-01 03-02 03-03 03-04; do
  su -c "SELECT id, customer_id, amount FROM shop.orders WHERE date_trunc('day', created_at) = '2025-$d'" >/dev/null
done

dsn() { echo "postgres://$1:$1-pw@127.0.0.1:$PORT/$DB"; }
# t LABEL USER EXPECTED_EXIT PATTERN args...   runs pqn as USER, checks the exit code and that the output matches
t() {
  label="$1"; user="$2"; want="$3"; pat="$4"; shift 4
  set +e
  out="$(PQN_DSN="$(dsn "$user")" "$PQN" "$@" 2>&1)"; code=$?
  set -e
  if [ "$code" != "$want" ]; then bad "$label -> exit $code, wanted $want. Output: $(echo "$out" | head -5)"
  elif ! echo "$out" | grep -qE "$pat"; then bad "$label -> output does not match '$pat'. Output: $(echo "$out" | head -8)"
  else ok "$label"; fi
  LAST="$out"
}

echo "== Setup and workload, from a terminal"
t "doctor: a healthy setup exits 0" carol 0 "0 blocking" doctor
t "doctor: an analyst may not read the safety report" alice 1 "permission denied" doctor
t "top ranks the application's slow statement" alice 0 "date_trunc" top -n 5
QID="$(PQN_DSN="$(dsn alice)" "$PQN" top --json -n 20 | python3 -c "import json,sys; d=json.load(sys.stdin); print([r['queryid'] for r in d if 'date_trunc' in r['query']][0])")"
[ -n "$QID" ] && ok "the queryid of the slow statement was found ($QID)" || bad "no queryid"

echo "== The pitch: propose from the plan, then prove it"
t "investigate --queryid proposes a rewrite and proves it" alice 0 "Proven" investigate --queryid="$QID" --bind day --bind 2025-03-01 --title "daily orders"
for want in "What the plan shows" "seq_scan" "function_wrap" "Proposed from the plan" "created_at >=" "same rows" "Evidence is in the ledger"; do
  echo "$LAST" | grep -q "$want" && ok "the report contains '$want'" || bad "the report is missing '$want'"
done
echo "$LAST" | grep -qE "[0-9.]+x" && ok "the report shows a measured speedup" || bad "no speedup shown"
t "a statement with placeholders and no --bind is planned but never executed" alice 2 "Pass --bind" investigate --queryid="$QID" --no-record
t "investigate a pasted statement" alice 0 "Proven" investigate --sql "SELECT id FROM shop.orders WHERE to_char(created_at, 'YYYY-MM') = '2025-05'" --title "monthly"
t "investigate over the replica code path (measure there, record on the primary)" alice 0 "recorded on the primary" investigate --replica "$(dsn alice)" --sql "SELECT id FROM shop.orders WHERE created_at::date = '2025-06-15'"
t "an index proposal is offered and labelled review only" alice 2 "review only" investigate --no-record --sql "SELECT id FROM shop.orders WHERE customer_id = 42 AND amount > 100"
t "--json is machine readable" alice 0 '"proven": 1' investigate --json --no-record --sql "SELECT id FROM shop.orders WHERE date_trunc('day', created_at) = '2025-03-09'"
echo "$LAST" | python3 -c "import json,sys; d=json.load(sys.stdin); assert d['candidates'][0]['verdict']=='Proven'" && ok "the JSON parses and carries the verdict" || bad "the JSON is malformed"

echo "== A wrong rewrite is never an improvement"
BEFORE="SELECT id FROM shop.orders WHERE date_trunc('day', created_at) = '2025-03-01'"
t "prove: a rewrite that returns other rows is Different, exit 2" alice 2 "Verdict: Different" prove --before "$BEFORE" --after "SELECT id FROM shop.orders WHERE created_at >= '2025-03-01' AND created_at < '2025-03-03'"
t "prove: the correct rewrite is Proven, exit 0" alice 0 "Verdict: Proven" prove --before "$BEFORE" --after "SELECT id FROM shop.orders WHERE created_at >= '2025-03-01' AND created_at < '2025-03-02'"
t "prove: a slower rewrite is NotFaster, exit 2" alice 2 "NotFaster" prove --before "SELECT id FROM shop.orders WHERE created_at >= '2025-03-01' AND created_at < '2025-03-02'" --after "$BEFORE"

echo "== The ledger"
t "investigations lists your work" alice 0 "daily orders" investigations
ID="$(PQN_DSN="$(dsn alice)" "$PQN" investigations --json | python3 -c "import json,sys; d=json.load(sys.stdin); print([r['id'] for r in d if r['title']=='daily orders'][0])")"
t "evidence shows the plan, the findings and the proof" alice 0 "proof by alice" evidence "$ID"
echo "$LAST" | grep -q "plan by alice" && echo "$LAST" | grep -q "findings by alice" && ok "the plan and the findings are recorded too" || bad "plan or findings missing"
t "a viewer sees none of another user's investigations" bob 0 "^id +when" investigations
[ "$(echo "$LAST" | wc -l | tr -d ' ')" = "2" ] && ok "a viewer's list is empty" || bad "a viewer saw rows: $LAST"
t "an admin sees everyone's" carol 0 "daily orders" investigations
t "a viewer cannot investigate" bob 1 "permission denied" investigate --no-record --sql "SELECT 1 FROM shop.orders LIMIT 1"
t "someone in no group is refused everything" eve 1 "permission denied" top

echo "== The limits hold from a terminal"
t "run over the curated view works, with plain names" alice 0 "\(4 row" run --sql "SELECT status, count(*) AS n FROM orders GROUP BY 1 ORDER BY 1"
t "a hidden column is refused by the database" alice 1 "permission denied|does not exist" run --sql "SELECT notes FROM shop.orders LIMIT 1"
t "DELETE is refused by the tool before it is sent" alice 1 "not accepted" run --sql "DELETE FROM orders"
t "a server-side file read is refused by the tool" alice 1 "not accepted" run --sql "SELECT pg_read_file('/etc/passwd')"
t "stacked statements are refused" alice 1 "not accepted" run --sql "SELECT 1; SELECT 2"
t "plan works for SELECT * because the table is exposed with scope full" alice 0 "Estimated plan" plan --sql "SELECT * FROM shop.orders WHERE id = 1"
[ "$(su -c "SELECT count(*) FROM shop.orders")" = "800000" ] && ok "no row was changed or deleted" || bad "the table changed"
[ "$(su -c "SELECT count(*) FROM pg_indexes WHERE schemaname = 'shop'")" = "2" ] && ok "no index was created" || bad "an index appeared"

echo
echo "RESULT pqn cli: pass=$PASS fail=$FAIL"
[ "$FAIL" -eq 0 ]
