#!/usr/bin/env bash
# A user story, end to end, on a plain PostgreSQL with heavy data.
#
# A team runs PostgreSQL with an application that has slow queries. They install the pqn extension into
# their running database, find the slow statements, get rewrites and index advice, apply them, and
# measure the difference. Nothing of ours is in the database until the install step.
#
#   1. a plain postgres:18 with ~17 million rows and an application workload (slow)
#   2. install: copy 3 files, preload pg_stat_statements (one restart), CREATE EXTENSION pqn, init()
#   3. run the workload again and rank it with pqn top
#   4. investigate the worst statements with pqn (findings, a rewrite from the plan, verified)
#   5. apply the fixes (the rewrites, and the indexes pqn advised) and run the workload again
#
# It takes about ten minutes and about 4 GB of disk. Not part of CI. Needs Docker, and bin/pqn (make build-pqn).
# Usage: bash tools/db/pqn-heavy-scenario.sh          (PQN_SCENARIO_KEEP=1 keeps the container)
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT_DIR"
C="${PQN_SCENARIO_CONTAINER:-pqn-user-db}"
PORT="${PQN_SCENARIO_PORT:-5437}"
PG_IMAGE="${PG_IMAGE:-postgres:18}"
ORDERS="${ORDERS:-5000000}"
ITEMS="${ITEMS:-12000000}"
SECS="${SECS:-40}"
PQN="${PQN_BIN:-$ROOT_DIR/bin/pqn}"
DB=shopdb
VOL="${C}-data"
[ -x "$PQN" ] || { echo "build the tool first: make build-pqn" >&2; exit 1; }

cleanup() { [ "${PQN_SCENARIO_KEEP:-}" = 1 ] || { docker rm -f "$C" >/dev/null 2>&1 || true; docker volume rm "$VOL" >/dev/null 2>&1 || true; }; rm -rf "$TMP"; }
TMP="$(mktemp -d /tmp/pqn-scenario.XXXXXX)"
trap cleanup EXIT INT TERM
pg() { docker exec -i "$C" psql -X -q -At -U postgres -d "$DB" "$@"; }
step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
tstamp() { date +%s; }

step "1. The team's database: plain $PG_IMAGE, no pqn, no pg_stat_statements"
docker rm -f "$C" >/dev/null 2>&1 || true
docker volume rm "$VOL" >/dev/null 2>&1 || true
docker run -d --name "$C" -p "127.0.0.1:$PORT:5432" -v "$VOL:/var/lib/postgresql" \
  -e POSTGRES_PASSWORD=secret -e POSTGRES_DB=$DB --shm-size=1g "$PG_IMAGE" >/dev/null
wait_ready() { until docker exec "$C" pg_isready -U postgres -h 127.0.0.1 -d $DB >/dev/null 2>&1; do sleep 1; done; sleep 2; }
wait_ready
pg -c "select version()" | cut -c1-40
pg -c "select count(*) || ' pqn extensions available' from pg_available_extensions where name = 'pqn'"

step "   heavy data: customers, products, $ORDERS orders, $ITEMS order items"
t0=$(tstamp)
pg <<SQL
CREATE SCHEMA shop;
CREATE TABLE shop.customers AS
  SELECT g AS id, 'customer ' || g AS name,
         CASE WHEN g % 9 = 0 THEN NULL ELSE 'c' || g || '@example.test' END AS email,
         (ARRAY['eu','us','apac','latam'])[1 + g % 4] AS region,
         timestamptz '2023-01-01' + (g % 700) * interval '1 day' AS created_at
    FROM generate_series(1, 300000) g;
ALTER TABLE shop.customers ADD PRIMARY KEY (id);
CREATE TABLE shop.products AS
  SELECT g AS id, (ARRAY['books','toys','tools','garden','kitchen','sport'])[1 + g % 6] AS category,
         'product ' || g AS name, ((g * 37) % 9000) / 10.0 AS price
    FROM generate_series(1, 20000) g;
ALTER TABLE shop.products ADD PRIMARY KEY (id);
CREATE TABLE shop.orders AS
  SELECT g AS id, (1 + (g::bigint * 7919) % 300000)::int AS customer_id,
         timestamptz '2024-01-01' + ((g::bigint * 13) % 730)::int * interval '1 day' + (g % 86400) * interval '1 second' AS created_at,
         (ARRAY['paid','new','refunded','shipped'])[1 + g % 4] AS status,
         ((g::bigint * 31) % 99973)::numeric / 100 AS total,
         CASE WHEN g % 7 = 0 THEN NULL ELSE 'note ' || g END AS notes
    FROM generate_series(1, $ORDERS) g;
ALTER TABLE shop.orders ADD PRIMARY KEY (id);
CREATE INDEX orders_created_idx ON shop.orders (created_at);
CREATE INDEX orders_status_idx ON shop.orders (status);
CREATE TABLE shop.order_items AS
  SELECT g AS id, (1 + (g::bigint * 7) % $ORDERS)::int AS order_id,
         (1 + (g::bigint * 131) % 20000)::int AS product_id, (1 + g % 5) AS qty,
         ((g * 37) % 9000) / 10.0 AS price
    FROM generate_series(1, $ITEMS) g;
ALTER TABLE shop.order_items ADD PRIMARY KEY (id);
ANALYZE shop.customers; ANALYZE shop.products; ANALYZE shop.orders; ANALYZE shop.order_items;
SQL
pg -c "select 'database size ' || pg_size_pretty(pg_database_size('$DB')) || ', orders ' || (select count(*) from shop.orders) || ', order_items ' || (select count(*) from shop.order_items)"
echo "   built in $(( $(tstamp) - t0 )) s"

# The application's queries. The values change on every call, like a real workload.
mkdir -p "$TMP/w"
cat > "$TMP/w/1_monthly_revenue.sql" <<'EOF'
\set m random(0, 23)
SELECT date_trunc('day', created_at) AS day, sum(total) FROM shop.orders WHERE date_trunc('month', created_at) = date '2024-01-01' + (:m * interval '1 month') GROUP BY 1;
EOF
cat > "$TMP/w/2_orders_on_a_day.sql" <<'EOF'
\set d random(0, 729)
SELECT id, customer_id, total FROM shop.orders WHERE created_at::date = date '2024-01-01' + :d;
EOF
cat > "$TMP/w/3_customer_history.sql" <<'EOF'
\set c random(1, 300000)
SELECT id, created_at, total FROM shop.orders WHERE customer_id = :c ORDER BY created_at DESC LIMIT 20;
EOF
cat > "$TMP/w/4_order_lines.sql" <<'EOF'
\set o random(1, 5000000)
SELECT product_id, sum(qty) FROM shop.order_items WHERE order_id = :o GROUP BY product_id;
EOF
cat > "$TMP/w/5_orders_by_month_text.sql" <<'EOF'
\set m random(1, 12)
SELECT count(*) FROM shop.orders WHERE to_char(created_at, 'YYYY-MM') = '2025-' || lpad(:m::text, 2, '0');
EOF
cat > "$TMP/w/6_order_by_id.sql" <<'EOF'
\set i random(1, 5000000)
SELECT id, status, total FROM shop.orders WHERE id = :i;
EOF
docker cp "$TMP/w" "$C:/tmp/w" >/dev/null
# weights: the slow queries are a minority of calls, and most of the time
run_workload() {
  local dir="$1"; shift
  docker exec "$C" pgbench -n -r -U postgres -d $DB -c 4 -j 4 -T "$SECS" \
    -f "$dir/1_monthly_revenue.sql@1" -f "$dir/2_orders_on_a_day.sql@2" -f "$dir/3_customer_history.sql@3" \
    -f "$dir/4_order_lines.sql@3" -f "$dir/5_orders_by_month_text.sql@1" -f "$dir/6_order_by_id.sql@20" 2>&1
}
latencies() { awk '/^ +[0-9.]+ +[0-9]+ +.*shop\./ { printf "%9s ms  %s\n", $1, substr($0, index($0, $3), 88) }' ; }

step "   the application runs for ${SECS}s. Is it slow? (no tool involved yet)"
run_workload /tmp/w > "$TMP/before.txt"
grep -E "tps|latency average" "$TMP/before.txt" | head -3
echo "   per-statement average latency:"; latencies < "$TMP/before.txt"

step "2. Install the extension into the running database (what docs/getting-started/pqn-installation.md says)"
SHARE="$(docker exec "$C" pg_config --sharedir)/extension"
for f in pqn.control pqn--1.0.sql pqn--1.0--1.1.sql; do docker cp "infra/pqn-extension/$f" "$C:$SHARE/"; done
pg -c "select name || ' ' || default_version || ' is available' from pg_available_extensions where name = 'pqn'"
echo "   preload pg_stat_statements (one restart)"
pg -c "ALTER SYSTEM SET shared_preload_libraries = 'pg_stat_statements'" >/dev/null
docker restart "$C" >/dev/null; wait_ready
pg -c "CREATE EXTENSION pg_stat_statements" -c "CREATE EXTENSION pqn"
pg -c "SELECT pqn_api.init()"
echo "   expose the tables (scope full: real statements use every column) and add two people"
for t in "customers:'id','name','region'" "products:'id','category','price'" "orders:'id','customer_id','created_at','status','total'" "order_items:'id','order_id','product_id','qty','price'"; do
  pg -c "SELECT pqn_api.expose('shop.${t%%:*}', ARRAY[${t#*:}], '${t%%:*}', 'full') IS NOT NULL" >/dev/null
done
pg -c "CREATE ROLE alice LOGIN PASSWORD 'alice-pw'" -c "CREATE ROLE carol LOGIN PASSWORD 'carol-pw'"
pg -c "SELECT pqn_api.enroll('alice', 'analyst', '300s') IS NOT NULL" -c "SELECT pqn_api.enroll('carol', 'admin') IS NOT NULL" >/dev/null
A="postgres://alice:alice-pw@127.0.0.1:$PORT/$DB"; D="postgres://carol:carol-pw@127.0.0.1:$PORT/$DB"
PQN_DSN="$D" "$PQN" doctor | tail -1

step "3. The application runs again, now that statements are recorded"
pg -c "SELECT pg_stat_statements_reset()" >/dev/null
run_workload /tmp/w > "$TMP/second.txt"
grep -E "tps" "$TMP/second.txt" | head -1
export PQN_DSN="$A"
"$PQN" top -n 8

step "4. Investigate the worst statements"
inv() { # inv TITLE SQL   (a concrete example of the statement, as the team would paste it)
  echo; echo "----- $1"
  "$PQN" investigate --title "$1" --sql "$2" > "$TMP/inv.txt" 2>&1 || true
  { sed -n '/^Statement/,/^What the plan shows/p' "$TMP/inv.txt" | sed -n '2,3p'
    printf '   finding: '; grep -E '^  \[(high|medium)\]' "$TMP/inv.txt" | grep -v 'high_cost\|index_health' | head -1 | sed 's/^  //'
    sed -n '/^  1\. /,/^Proof\|^Verdict\|^  index/p' "$TMP/inv.txt" | grep -vE '^Proof|^Verdict|^  index|^$' | sed -n '1p;4,7p'
    grep -E '^1  function_wrap|^1  .*(Proven|NotFaster|Different|Unverified)$' "$TMP/inv.txt" | head -1 | sed 's/^/   proof:   /'
    grep -E 'CREATE INDEX' "$TMP/inv.txt" | head -1 | sed 's/^ */   index advice (review only): /'
  } || true
}
inv "monthly revenue (date_trunc on created_at)" "SELECT date_trunc('day', created_at) AS day, sum(total) FROM shop.orders WHERE date_trunc('month', created_at) = date '2025-03-01' GROUP BY 1"
inv "orders on a day (::date cast)" "SELECT id, customer_id, total FROM shop.orders WHERE created_at::date = date '2025-06-15'"
inv "orders by month (to_char)" "SELECT count(*) FROM shop.orders WHERE to_char(created_at, 'YYYY-MM') = '2025-03'"
inv "customer history (no index on customer_id)" "SELECT id, created_at, total FROM shop.orders WHERE customer_id = 4242 ORDER BY created_at DESC LIMIT 20"
inv "order lines (no index on order_id)" "SELECT product_id, sum(qty) FROM shop.order_items WHERE order_id = 1234567 GROUP BY product_id"

step "5. Apply the fixes: the rewrites pqn proved, and the indexes it advised"
echo "   the two indexes (built by the team, on their own schedule)"
t0=$(tstamp)
pg -c "CREATE INDEX CONCURRENTLY orders_customer_idx ON shop.orders (customer_id, created_at)"
pg -c "CREATE INDEX CONCURRENTLY order_items_order_idx ON shop.order_items (order_id)"
pg -c "ANALYZE shop.orders; ANALYZE shop.order_items"
echo "   built in $(( $(tstamp) - t0 )) s"
echo "   pqn plan on the two statements again (estimated cost, findings):"
for q in "SELECT id, created_at, total FROM shop.orders WHERE customer_id = 4242 ORDER BY created_at DESC LIMIT 20" "SELECT product_id, sum(qty) FROM shop.order_items WHERE order_id = 1234567 GROUP BY product_id"; do
  "$PQN" plan --sql "$q" | sed -n '1,2p' | tr '\n' ' ' | cut -c1-150; echo
done
cat > "$TMP/w/1_monthly_revenue.sql" <<'EOF'
\set m random(0, 23)
SELECT date_trunc('day', created_at) AS day, sum(total) FROM shop.orders WHERE created_at >= date '2024-01-01' + (:m * interval '1 month') AND created_at < date '2024-01-01' + ((:m + 1) * interval '1 month') GROUP BY 1;
EOF
cat > "$TMP/w/2_orders_on_a_day.sql" <<'EOF'
\set d random(0, 729)
SELECT id, customer_id, total FROM shop.orders WHERE created_at >= date '2024-01-01' + :d AND created_at < date '2024-01-01' + (:d + 1);
EOF
cat > "$TMP/w/5_orders_by_month_text.sql" <<'EOF'
\set m random(1, 12)
SELECT count(*) FROM shop.orders WHERE created_at >= date '2025-01-01' + ((:m - 1) * interval '1 month') AND created_at < date '2025-01-01' + (:m * interval '1 month');
EOF
docker exec "$C" rm -rf /tmp/w2; docker cp "$TMP/w" "$C:/tmp/w2" >/dev/null
step "   the application runs again with the fixes"
run_workload /tmp/w2 > "$TMP/after.txt"
grep -E "tps|latency average" "$TMP/after.txt" | head -3
echo "   per-statement average latency:"; latencies < "$TMP/after.txt"

step "6. Before and after"
paste -d'|' <(latencies < "$TMP/before.txt" | awk '{print $1}') <(latencies < "$TMP/after.txt" | awk '{print $1}') <(latencies < "$TMP/after.txt" | cut -c14-70) \
  | awk -F'|' 'BEGIN{printf "%10s  %10s  %8s   %s\n","before ms","after ms","faster","statement"} {sp=($2>0)?$1/$2:0; printf "%10s  %10s  %7.1fx   %s\n",$1,$2,sp,$3}'
echo; echo -n "throughput before: "; grep -m1 "^tps" "$TMP/before.txt"; echo -n "throughput after:  "; grep -m1 "^tps" "$TMP/after.txt"
step "   the ledger keeps what pqn found"
"$PQN" investigations -n 6
echo; echo "done. Nothing was created in the database by pqn except its own ledger."
