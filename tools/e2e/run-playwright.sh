#!/usr/bin/env bash
# Starts PgQueryNarrative server and runs Playwright browser tests.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

PORT="${PLAYWRIGHT_PORT:-18088}"
MOCK_PORT="${MOCK_OIDC_PORT:-19999}"
MOCK_OLLAMA_PORT="${MOCK_OLLAMA_PORT:-11435}"
BASE_URL="http://127.0.0.1:${PORT}"
MOCK_ADDR="127.0.0.1:${MOCK_PORT}"
MOCK_OLLAMA_ADDR="127.0.0.1:${MOCK_OLLAMA_PORT}"
OIDC_MODE="${PLAYWRIGHT_OIDC:-0}"

cleanup() {
  kill "${SERVER_PID:-}" "${MOCK_PID:-}" "${MOCK_OLLAMA_PID:-}" 2>/dev/null || true
}
trap cleanup EXIT

echo "Building frontend..."
(cd frontend && npm ci && npm run build)

echo "Generating API code..."
make generate

echo "Building server and mock Ollama..."
CGO_ENABLED=1 go build -o bin/playwright-server ./cmd/server
go build -o bin/mockollama ./cmd/mockollama

export DATABASE_HOST="${DATABASE_HOST:-localhost}"
export DATABASE_PORT="${DATABASE_PORT:-5432}"
export DATABASE_USER="${DATABASE_USER:-pgquerynarrative_app}"
export DATABASE_PASSWORD="${DATABASE_PASSWORD:-pgquerynarrative_app}"
export DATABASE_NAME="${DATABASE_NAME:-pgquerynarrative}"
export DATABASE_READONLY_USER="${DATABASE_READONLY_USER:-pgquerynarrative_readonly}"
export DATABASE_READONLY_PASSWORD="${DATABASE_READONLY_PASSWORD:-pgquerynarrative_readonly}"
export PGQUERYNARRATIVE_PORT="${PORT}"
export SECURITY_SESSION_SECRET="${SECURITY_SESSION_SECRET:-playwright-session-secret-32-chars!!}"
# Enable public share links for browser E2E (disabled by default; forbidden in production validate).
export SECURITY_SHARE_LINKS_ENABLED="${SECURITY_SHARE_LINKS_ENABLED:-true}"

# Seed demo.sales so critical-path queries have data (idempotent inserts).
seed_demo() {
  echo "Seeding demo data..."
  if docker compose exec -T postgres psql -U postgres -d pgquerynarrative -f - < tools/db/seed.sql >/tmp/pgqn-playwright-seed.log 2>&1; then
    echo "Seed via docker compose OK"
    return 0
  fi
  if command -v psql >/dev/null 2>&1; then
    local db_url="${DATABASE_URL:-postgres://postgres:postgres@localhost:5432/pgquerynarrative?sslmode=disable}"
    if psql "$db_url" -f ./tools/db/seed.sql >/tmp/pgqn-playwright-seed.log 2>&1; then
      echo "Seed via psql OK"
      return 0
    fi
  fi
  echo "WARNING: demo seed failed (queries may return 0 rows). Log:"
  tail -20 /tmp/pgqn-playwright-seed.log || true
}
seed_demo

echo "Starting mock Ollama on ${MOCK_OLLAMA_ADDR}..."
MOCK_OLLAMA_ADDR="${MOCK_OLLAMA_ADDR}" ./bin/mockollama >/tmp/pgqn-mockollama.log 2>&1 &
MOCK_OLLAMA_PID=$!
for _ in $(seq 1 30); do
  if curl -sf "http://${MOCK_OLLAMA_ADDR}/health" >/dev/null; then
    break
  fi
  sleep 0.2
done
curl -sf "http://${MOCK_OLLAMA_ADDR}/health" >/dev/null || {
  echo "mock Ollama failed to start; log:"
  tail -50 /tmp/pgqn-mockollama.log || true
  exit 1
}
export LLM_PROVIDER=ollama
export LLM_BASE_URL="http://${MOCK_OLLAMA_ADDR}"
export LLM_MODEL="${LLM_MODEL:-playwright}"

if [[ "$OIDC_MODE" == "1" ]]; then
  export MOCK_OIDC_ADDR="${MOCK_ADDR}"
  export MOCK_OIDC_AUDIENCE="${MOCK_OIDC_AUDIENCE:-pgquerynarrative}"
  export MOCK_OIDC_CLIENT_ID="${MOCK_OIDC_CLIENT_ID:-e2e-client}"
  echo "Starting mock OIDC on ${MOCK_ADDR}..."
  go run ./cmd/mockoidc &
  MOCK_PID=$!
  for _ in $(seq 1 30); do
    if curl -sf "http://${MOCK_ADDR}/.well-known/openid-configuration" >/dev/null; then
      break
    fi
    sleep 0.5
  done
  curl -sf "http://${MOCK_ADDR}/.well-known/openid-configuration" >/dev/null || {
    echo "mock OIDC failed to start"
    exit 1
  }
  export SECURITY_AUTH_ENABLED=true
  export SECURITY_OIDC_ISSUER="http://${MOCK_ADDR}"
  export SECURITY_OIDC_CLIENT_ID="${MOCK_OIDC_CLIENT_ID}"
  export SECURITY_OIDC_CLIENT_SECRET="${MOCK_OIDC_CLIENT_SECRET:-e2e-secret}"
  export SECURITY_OIDC_REDIRECT_URL="${BASE_URL}/auth/callback"
  export SECURITY_OIDC_AUDIENCE="${MOCK_OIDC_AUDIENCE}"
  # Local mock IdP has no IdP-group→org mapping; allow first login to join the default org.
  export SECURITY_OIDC_AUTO_JOIN_DEFAULT_ORG=true
else
  export SECURITY_AUTH_ENABLED=false
fi

./bin/playwright-server >/tmp/pgqn-playwright-server.log 2>&1 &
SERVER_PID=$!

for _ in $(seq 1 60); do
  if curl -sf "${BASE_URL}/health" >/dev/null; then
    break
  fi
  sleep 1
done
curl -sf "${BASE_URL}/health" >/dev/null || {
  echo "Server failed to start; log:"
  tail -50 /tmp/pgqn-playwright-server.log || true
  exit 1
}

echo "Running Playwright against ${BASE_URL} (OIDC=${OIDC_MODE})..."
cd test/playwright
npm ci
npx playwright install chromium
export PLAYWRIGHT_BASE_URL="${BASE_URL}"
export PLAYWRIGHT_OIDC="${OIDC_MODE}"
if [[ "$OIDC_MODE" == "1" ]]; then
  npm run test
else
  npm run test:smoke
fi
