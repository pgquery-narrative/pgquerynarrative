#!/usr/bin/env bash
# Runs measurable internal-pilot acceptance checks against Docker Postgres and Go tests.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

PASSED=0
FAILED=0
SKIPPED=0
RESULTS=()

record() {
  local status="$1"
  local name="$2"
  local detail="${3:-}"
  case "$status" in
    pass) PASSED=$((PASSED + 1)); RESULTS+=("PASS|$name|$detail") ;;
    fail) FAILED=$((FAILED + 1)); RESULTS+=("FAIL|$name|$detail") ;;
    skip) SKIPPED=$((SKIPPED + 1)); RESULTS+=("SKIP|$name|$detail") ;;
  esac
}

echo "=== PgQueryNarrative internal pilot acceptance ==="
echo "Started: $(date -u +"%Y-%m-%dT%H:%M:%SZ")"
echo ""

# 1. Postgres + migrations
echo "--- Infrastructure ---"
if make postgres-up >/dev/null 2>&1; then
  record pass "postgres-up" "container ready"
else
  record fail "postgres-up" "docker compose failed"
fi

if make migrate-docker >/dev/null 2>&1; then
  MIGRATE_MSG="migrations applied"
else
  MIGRATE_MSG="migrate command returned non-zero"
fi

DIRTY="$(docker compose exec -T postgres psql -U postgres -d pgquerynarrative -tAc 'SELECT dirty FROM schema_migrations LIMIT 1' 2>/dev/null | tr -d '[:space:]' || true)"
REQUIRED="$(grep 'RequiredMigrationVersion uint' app/db/migrations_check.go | sed -E 's/.*= ([0-9]+).*/\1/')"
if [[ "$DIRTY" == "t" ]]; then
  echo "⚠️  schema_migrations dirty; refusing automatic force (required version ${REQUIRED:-unknown}). Resolve manually with migrate force."
  record fail "migrate-docker" "dirty schema_migrations; manual force required (do not auto-force)"
fi

VERSION="$(docker compose exec -T postgres psql -U postgres -d pgquerynarrative -tAc 'SELECT version FROM schema_migrations LIMIT 1' 2>/dev/null | tr -d '[:space:]' || true)"
MIGRATE_MSG="${MIGRATE_MSG:-applied}"
if [[ "$DIRTY" != "t" && -n "$VERSION" && -n "$REQUIRED" && "$VERSION" -ge "$REQUIRED" ]]; then
  record pass "migrate-docker" "${MIGRATE_MSG}; version=$VERSION"
else
  if [[ "$DIRTY" != "t" ]]; then
    record fail "migrate-docker" "${MIGRATE_MSG}; got version=${VERSION:-none}"
  fi
fi

if [[ "$DIRTY" != "t" && -n "$VERSION" && -n "$REQUIRED" && "$VERSION" -ge "$REQUIRED" ]]; then
  record pass "migration-version" "schema_migrations.version=$VERSION (required >= $REQUIRED)"
else
  if [[ "$DIRTY" != "t" ]]; then
    record fail "migration-version" "got=${VERSION:-none} required>=${REQUIRED:-?}"
  fi
fi

# 2. Readonly role write block
if docker compose exec -T postgres psql -U pgquerynarrative_readonly -d pgquerynarrative -c "INSERT INTO demo.sales DEFAULT VALUES;" >/dev/null 2>&1; then
  record fail "readonly-write-block" "INSERT succeeded unexpectedly"
else
  record pass "readonly-write-block" "read-only transaction enforced"
fi

if make db-security-verify-docker >/dev/null 2>&1; then
  record pass "db-security-verify" "readonly role boundary script"
else
  record fail "db-security-verify" "tools/db/verify_security.sh failed"
fi

# 3. Automated pilot integration tests (testcontainers)
echo ""
echo "--- Automated tests ---"
if CGO_ENABLED=1 go test ./test/integration/... -run 'Pilot_|Membership|ScheduleClaim|ReadOnly|Governance|Session|Budget|Webhook|OIDC' -count=1 -timeout 10m; then
  record pass "integration-pilot-tests" "pilot + security integration suite"
else
  record fail "integration-pilot-tests" "see test output above"
fi

if make test-unit >/dev/null 2>&1; then
  record pass "unit-tests" "make test-unit"
else
  record fail "unit-tests" "unit test failures"
fi

# 4. Build + HTTP smoke (optional if port free)
echo ""
echo "--- Runtime smoke ---"
if CGO_ENABLED=1 go build -o bin/server ./cmd/server >/dev/null 2>&1; then
  record pass "build" "go build ./cmd/server"
else
  record fail "build" "go build failed"
fi

SMOKE_PORT="${PILOT_SMOKE_PORT:-18080}"
if ! lsof -i ":${SMOKE_PORT}" >/dev/null 2>&1; then
  export DATABASE_HOST=localhost
  export DATABASE_PORT=5432
  export DATABASE_USER=pgquerynarrative_app
  export DATABASE_PASSWORD=pgquerynarrative_app
  export DATABASE_NAME=pgquerynarrative
  export DATABASE_READONLY_USER=pgquerynarrative_readonly
  export DATABASE_READONLY_PASSWORD=pgquerynarrative_readonly
  export SECURITY_AUTH_ENABLED=false
  export PGQUERYNARRATIVE_PORT="${SMOKE_PORT}"

  ./bin/server >/tmp/pgqn-pilot-smoke.log 2>&1 &
  SMOKE_PID=$!
  trap 'kill ${SMOKE_PID:-} 2>/dev/null || true' EXIT

  ready=0
  for _ in $(seq 1 30); do
    if curl -sf "http://127.0.0.1:${SMOKE_PORT}/ready" >/dev/null 2>&1; then
      ready=1
      break
    fi
    sleep 1
  done

  if [[ "$ready" -eq 1 ]]; then
    record pass "ready-endpoint" "/ready returned 200"
    curl -sf "http://127.0.0.1:${SMOKE_PORT}/health" >/dev/null 2>&1 || true
    METRICS_BODY="$(curl -sf "http://127.0.0.1:${SMOKE_PORT}/metrics?format=prometheus" 2>/dev/null || true)"
    if echo "$METRICS_BODY" | grep -q 'pgqn_http_requests_total'; then
      record pass "metrics-endpoint" "Prometheus counters exposed"
    else
      record fail "metrics-endpoint" "missing pgqn_http_requests_total"
    fi
  else
    record fail "ready-endpoint" "server did not become ready; see /tmp/pgqn-pilot-smoke.log"
  fi
  kill "$SMOKE_PID" 2>/dev/null || true
  trap - EXIT
else
  record skip "runtime-smoke" "port ${SMOKE_PORT} in use"
fi

# 5. OIDC staging (corporate IdP when configured)
echo ""
echo "--- OIDC staging ---"
if [ -n "${SECURITY_OIDC_ISSUER:-}" ]; then
  if bash tools/ops/oidc_staging_validate.sh; then
    record pass "oidc-staging" "corporate IdP discovery + JWKS validated"
  else
    record fail "oidc-staging" "see OIDC staging output above"
  fi
else
  if CGO_ENABLED=1 go test ./test/integration/... -run TestPilot_OIDCCorporateFlow -count=1 -timeout 5m >/dev/null 2>&1; then
    record pass "oidc-mock-flow" "mock corporate IdP PKCE + session flow"
  else
    record fail "oidc-mock-flow" "mock OIDC integration test failed"
  fi
fi

# 6. Manual / ops items (document only)
record skip "oidc-browser-manual" "optional: verify /auth/login in corporate browser when SECURITY_OIDC_ISSUER is set"

echo ""
echo "--- Backup restore drill ---"
BACKUP="/tmp/pgqn-pilot-drill-$$.sql.gz"
DRILL_DB="pgqn_restore_drill_$$"
if chmod +x tools/ops/backup.sh && DATABASE_HOST=localhost DATABASE_USER=postgres DATABASE_PASSWORD=postgres tools/ops/backup.sh "$BACKUP" >/dev/null 2>&1; then
  if docker compose exec -T postgres psql -U postgres -c "CREATE DATABASE ${DRILL_DB};" >/dev/null 2>&1; then
    if gunzip -c "$BACKUP" | docker compose exec -T postgres psql -U postgres -d "$DRILL_DB" -v ON_ERROR_STOP=1 >/dev/null 2>&1; then
      APP_TABLES="$(docker compose exec -T postgres psql -U postgres -d "$DRILL_DB" -tAc "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='app'" 2>/dev/null | tr -d '[:space:]' || true)"
      MIG_VER="$(docker compose exec -T postgres psql -U postgres -d "$DRILL_DB" -tAc "SELECT version FROM public.schema_migrations LIMIT 1" 2>/dev/null | tr -d '[:space:]' || true)"
      if [[ -n "$APP_TABLES" && "$APP_TABLES" -gt 0 && -n "$MIG_VER" ]]; then
        record pass "backup-restore-drill" "restored ${APP_TABLES} app tables; schema_migrations.version=${MIG_VER} in ${DRILL_DB}"
      else
        record fail "backup-restore-drill" "restored database missing app tables or schema_migrations (tables=${APP_TABLES:-0} version=${MIG_VER:-none})"
      fi
    else
      record fail "backup-restore-drill" "psql restore into ${DRILL_DB} failed"
    fi
    docker compose exec -T postgres psql -U postgres -c "DROP DATABASE IF EXISTS ${DRILL_DB} WITH (FORCE);" >/dev/null 2>&1 || true
  else
    record fail "backup-restore-drill" "could not create drill database"
  fi
  rm -f "$BACKUP"
else
  record fail "backup-restore-drill" "backup failed (is postgres up and pg_dump installed?)"
fi

echo ""
echo "=== Summary ==="
printf '%s\n' "${RESULTS[@]}" | while IFS='|' read -r status name detail; do
  case "$status" in
    PASS) echo "✅ $name — $detail" ;;
    FAIL) echo "❌ $name — $detail" ;;
    SKIP) echo "⏭️  $name — $detail" ;;
  esac
done
echo ""
echo "Passed: $PASSED  Failed: $FAILED  Skipped: $SKIPPED"
echo "Finished: $(date -u +"%Y-%m-%dT%H:%M:%SZ")"

if [[ "$FAILED" -gt 0 ]]; then
  exit 1
fi
