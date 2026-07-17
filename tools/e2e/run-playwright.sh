#!/usr/bin/env bash
# Starts PgQueryNarrative server and runs Playwright browser tests.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

PORT="${PLAYWRIGHT_PORT:-18088}"
MOCK_PORT="${MOCK_OIDC_PORT:-19999}"
BASE_URL="http://127.0.0.1:${PORT}"
MOCK_ADDR="127.0.0.1:${MOCK_PORT}"
OIDC_MODE="${PLAYWRIGHT_OIDC:-0}"

cleanup() {
  kill "${SERVER_PID:-}" "${MOCK_PID:-}" 2>/dev/null || true
}
trap cleanup EXIT

echo "Building frontend..."
(cd frontend && npm ci && npm run build)

echo "Generating API code..."
make generate

echo "Building server..."
CGO_ENABLED=1 go build -o bin/playwright-server ./cmd/server

export DATABASE_HOST="${DATABASE_HOST:-localhost}"
export DATABASE_PORT="${DATABASE_PORT:-5432}"
export DATABASE_USER="${DATABASE_USER:-pgquerynarrative_app}"
export DATABASE_PASSWORD="${DATABASE_PASSWORD:-pgquerynarrative_app}"
export DATABASE_NAME="${DATABASE_NAME:-pgquerynarrative}"
export DATABASE_READONLY_USER="${DATABASE_READONLY_USER:-pgquerynarrative_readonly}"
export DATABASE_READONLY_PASSWORD="${DATABASE_READONLY_PASSWORD:-pgquerynarrative_readonly}"
export PGQUERYNARRATIVE_PORT="${PORT}"
export SECURITY_SESSION_SECRET="${SECURITY_SESSION_SECRET:-playwright-session-secret-32-chars!!}"

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
