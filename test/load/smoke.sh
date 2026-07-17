#!/usr/bin/env bash
# Lightweight load smoke: health, ready, and optional authenticated query endpoint.
set -euo pipefail

BASE="${LOAD_BASE_URL:-http://127.0.0.1:8080}"
REQUESTS="${LOAD_REQUESTS:-50}"
API_KEY="${LOAD_API_KEY:-}"

pass=0
fail=0

check() {
  local name="$1"
  local url="$2"
  local extra_args=("${@:3}")
  local ok=0
  for _ in $(seq 1 "$REQUESTS"); do
    if curl -sf "${extra_args[@]}" "$url" >/dev/null; then
      ok=$((ok + 1))
    fi
  done
  if [[ "$ok" -eq "$REQUESTS" ]]; then
    echo "PASS $name ($REQUESTS/$REQUESTS)"
    pass=$((pass + 1))
  else
    echo "FAIL $name ($ok/$REQUESTS)"
    fail=$((fail + 1))
  fi
}

echo "Load smoke: $REQUESTS requests each against $BASE"
check "health" "${BASE}/health"
check "ready" "${BASE}/ready"

if [[ -n "$API_KEY" ]]; then
  check "query-run" "${BASE}/api/v1/queries/run" \
    -X POST -H "Content-Type: application/json" -H "Authorization: Bearer ${API_KEY}" \
    -d '{"sql":"SELECT 1 AS n","limit":1}'
else
  echo "SKIP query-run (set LOAD_API_KEY to include authenticated POST)"
fi

echo "Summary: pass=$pass fail=$fail"
[[ "$fail" -eq 0 ]]
