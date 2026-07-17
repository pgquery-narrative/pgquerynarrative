#!/usr/bin/env sh
# Validates corporate OIDC IdP staging configuration (discovery, JWKS, client/session settings).
set -eu

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

if [ -z "${SECURITY_OIDC_ISSUER:-}" ]; then
  echo "SKIP: SECURITY_OIDC_ISSUER not set"
  exit 0
fi

echo "== OIDC corporate IdP staging validation =="
echo "Issuer: ${SECURITY_OIDC_ISSUER}"

export OIDC_STAGING_VALIDATE=1
if CGO_ENABLED=1 go test ./test/integration/... -run TestPilot_OIDCRealStaging -count=1 -timeout 2m -v; then
  echo "OK: corporate IdP staging checks passed"
  exit 0
fi

echo "FAIL: corporate IdP staging validation failed" >&2
exit 1
