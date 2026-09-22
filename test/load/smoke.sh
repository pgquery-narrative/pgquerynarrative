#!/usr/bin/env bash
# Lightweight load smoke: health, ready, and the query-run endpoint. Reports
# real latency (min/p50/p95/max), not just pass/fail.
set -euo pipefail

BASE="${LOAD_BASE_URL:-http://127.0.0.1:8080}"
REQUESTS="${LOAD_REQUESTS:-50}"
API_KEY="${LOAD_API_KEY:-}"

pass=0
fail=0

# check name url [curl args...]
# bash-3.2-safe: macOS's stock /bin/bash is 3.2, where "${arr[@]}" on an empty
# array under `set -u` is an unbound-variable error, not an empty expansion
# (fixed in bash 4.4+). "${arr[@]+"${arr[@]}"}" expands to nothing when arr is
# empty and to the elements otherwise, on both old and new bash.
check() {
  local name="$1" url="$2"
  shift 2
  local extra_args=("$@")
  local ok=0
  local times=()
  for _ in $(seq 1 "$REQUESTS"); do
    local out
    if out=$(curl -sf -o /dev/null -w '%{time_total}' "${extra_args[@]+"${extra_args[@]}"}" "$url"); then
      ok=$((ok + 1))
      times+=("$out")
    fi
  done
  local latency=""
  if [[ ${#times[@]} -gt 0 ]]; then
    latency=$(printf '%s\n' "${times[@]+"${times[@]}"}" | sort -n | awk -v n="${#times[@]}" '
      { a[NR]=$1 }
      END {
        p50 = a[int((n-1)*0.50)+1]
        p95 = a[int((n-1)*0.95)+1]
        printf "min=%.3fs p50=%.3fs p95=%.3fs max=%.3fs", a[1], p50, p95, a[n]
      }')
  fi
  if [[ "$ok" -eq "$REQUESTS" ]]; then
    echo "PASS $name ($REQUESTS/$REQUESTS) $latency"
    pass=$((pass + 1))
  else
    echo "FAIL $name ($ok/$REQUESTS) $latency"
    fail=$((fail + 1))
  fi
}

echo "Load smoke: $REQUESTS requests each against $BASE"
check "health" "${BASE}/health"
check "ready" "${BASE}/ready"

# Try query-run whether or not a key is set: the default local/dev config runs
# with auth disabled (SECURITY_AUTH_ENABLED=false), where this request needs
# no Authorization header at all — skipping it whenever LOAD_API_KEY is unset
# meant the only endpoint that touches Postgres never ran in that (default)
# configuration. If the target actually requires auth and no key is given,
# curl gets a real 401 and this reports FAIL, which is the honest outcome.
query_run_args=(-X POST -H "Content-Type: application/json" -d '{"sql":"SELECT 1 AS n","limit":1}')
if [[ -n "$API_KEY" ]]; then
  query_run_args+=(-H "Authorization: Bearer ${API_KEY}")
fi
check "query-run" "${BASE}/api/v1/queries/run" "${query_run_args[@]}"

echo "Summary: pass=$pass fail=$fail"
[[ "$fail" -eq 0 ]]
