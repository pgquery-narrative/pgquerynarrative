#!/usr/bin/env bash
# API smoke for demo scenes: me, run, explain, stats, ask (if LLM), optional NYC.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

BASE_URL="${BASE_URL:-http://127.0.0.1:8080}"
SKIP_ASK="${SKIP_ASK:-0}"
FAIL=0

pass() { echo "  PASS: $1"; }
fail() { echo "  FAIL: $1" >&2; FAIL=1; }

echo "==> Demo smoke against $BASE_URL"

if ! curl -fsS "$BASE_URL/ready" >/dev/null 2>&1; then
  echo "ERROR: app not ready. Run ./tools/demo/bootstrap.sh first." >&2
  exit 1
fi

echo "-- /api/v1/me"
if curl -fsS "$BASE_URL/api/v1/me" | grep -q organization_id; then
  pass "me"
else
  fail "me"
fi

echo "-- /api/v1/me/organizations"
if curl -fsS "$BASE_URL/api/v1/me/organizations" | grep -q organizations; then
  pass "me/organizations"
else
  fail "me/organizations"
fi

echo "-- POST /api/v1/queries/run (Scene 1)"
RUN_JSON="$(curl -fsS -X POST "$BASE_URL/api/v1/queries/run" \
  -H 'Content-Type: application/json' \
  --data-binary @- <<'EOF' || true
{"sql":"SELECT product_category, SUM(total_amount) AS total FROM demo.sales GROUP BY product_category ORDER BY total DESC LIMIT 10","limit":10,"connection_id":"default"}
EOF
)"
if printf '%s' "$RUN_JSON" | grep -q '"row_count"'; then
  pass "queries/run"
else
  fail "queries/run: $RUN_JSON"
fi

echo "-- POST /api/v1/queries/explain (Scene 2)"
EXPLAIN_JSON="$(curl -fsS -X POST "$BASE_URL/api/v1/queries/explain" \
  -H 'Content-Type: application/json' \
  --data-binary @- <<'EOF' || true
{"sql":"SELECT product_category, SUM(total_amount) AS total FROM demo.sales WHERE region = 'North' GROUP BY product_category ORDER BY total DESC","connection_id":"default"}
EOF
)"
if printf '%s' "$EXPLAIN_JSON" | grep -qE '"findings"|total_cost'; then
  FINDING_HINT=""
  if printf '%s' "$EXPLAIN_JSON" | grep -qi 'seq'; then
    FINDING_HINT=" (seq-scan style findings present — good for 10M pitch)"
  fi
  pass "queries/explain$FINDING_HINT"
else
  fail "queries/explain: $EXPLAIN_JSON"
fi

echo "-- GET /api/v1/queries/stats (Scene 5)"
STATS_CODE="$(curl -sS -o /tmp/pgqn_stats.json -w '%{http_code}' "$BASE_URL/api/v1/queries/stats?order_by=total_time&limit=5" || true)"
if [[ "$STATS_CODE" == "200" ]]; then
  pass "queries/stats"
else
  echo "  WARN: queries/stats HTTP $STATS_CODE (pg_stat_statements may need postgres-recreate)"
fi

echo "-- write SQL rejected (Scene 8)"
WRITE_CODE="$(curl -sS -o /tmp/pgqn_write.json -w '%{http_code}' -X POST "$BASE_URL/api/v1/queries/run" \
  -H 'Content-Type: application/json' \
  --data-binary @- <<'EOF' || true
{"sql":"DELETE FROM demo.sales WHERE id = 1","limit":1,"connection_id":"default"}
EOF
)"
if [[ "$WRITE_CODE" == "200" ]]; then
  fail "DELETE should not succeed (HTTP 200)"
else
  pass "DELETE rejected (HTTP $WRITE_CODE)"
fi

if [[ "$SKIP_ASK" != "1" ]]; then
  echo "-- POST /api/v1/suggestions/ask (Scene 3, optional LLM)"
  ASK_CODE="$(curl -sS -o /tmp/pgqn_ask.json -w '%{http_code}' -X POST "$BASE_URL/api/v1/suggestions/ask" \
    -H 'Content-Type: application/json' \
    --data-binary @- <<'EOF' || true
{"question":"Which product categories drive North region revenue?","connection_id":"default"}
EOF
)"
  if [[ "$ASK_CODE" == "200" ]] && grep -qiE 'sql|select' /tmp/pgqn_ask.json 2>/dev/null; then
    pass "suggestions/ask"
  else
    echo "  WARN: Ask HTTP $ASK_CODE — run make ollama-up (pulls llama3.2 in compose Ollama)"
    head -c 300 /tmp/pgqn_ask.json 2>/dev/null; echo
  fi
else
  echo "-- skipping Ask (SKIP_ASK=1)"
fi

echo "-- optional NYC probe"
NYC_CODE="$(curl -sS -o /tmp/pgqn_nyc.json -w '%{http_code}' -X POST "$BASE_URL/api/v1/queries/run" \
  -H 'Content-Type: application/json' \
  --data-binary @- <<'EOF' || true
{"sql":"SELECT COUNT(*) AS n FROM opendata.yellow_trips","limit":1,"connection_id":"default"}
EOF
)"
if [[ "$NYC_CODE" == "200" ]]; then
  pass "opendata.yellow_trips reachable"
else
  echo "  WARN: NYC not available (HTTP $NYC_CODE) — use WITH_NYC=1 ./tools/demo/bootstrap.sh"
fi

echo
if [[ "$FAIL" -ne 0 ]]; then
  echo "==> Smoke finished with FAILURES"
  exit 1
fi
echo "==> Smoke PASSED (core scenes)"
echo "    Next: open http://localhost:8080 → Investigate"
