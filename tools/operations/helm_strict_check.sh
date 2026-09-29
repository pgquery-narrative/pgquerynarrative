#!/usr/bin/env bash
# Verifies Helm chart StrictMode gates: default values must fail; ci-values must render.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CHART="${ROOT}/deploy/helm/pgquerynarrative"

if ! command -v helm >/dev/null 2>&1; then
  echo "helm not installed; skipping chart render checks" >&2
  exit 0
fi

echo "==> default values must fail StrictMode secret validation"
if helm template pgqn-strict-check "${CHART}" >/tmp/pgqn-helm-default.yaml 2>/tmp/pgqn-helm-default.err; then
  echo "ERROR: helm template with default values succeeded; expected fail on empty secrets" >&2
  exit 1
fi
if ! grep -qiE 'databasePassword|apiKeyHash|sessionSecret' /tmp/pgqn-helm-default.err; then
  echo "ERROR: unexpected helm failure (wanted secret validation):" >&2
  cat /tmp/pgqn-helm-default.err >&2
  exit 1
fi
echo "    ok: $(tr '\n' ' ' </tmp/pgqn-helm-default.err | head -c 200)"

echo "==> ci-values must render"
helm template pgqn-strict-check "${CHART}" -f "${CHART}/ci-values.yaml" >/tmp/pgqn-helm-ci.yaml
grep -q 'SECURITY_API_KEY_HASH' /tmp/pgqn-helm-ci.yaml
grep -q 'SECURITY_RATE_LIMIT_FAILURE_MODE: "closed"' /tmp/pgqn-helm-ci.yaml
grep -q 'SECURITY_AUDIT_MODE: "required"' /tmp/pgqn-helm-ci.yaml
grep -q 'SCHEDULE_RUNNER_ENABLED: "false"' /tmp/pgqn-helm-ci.yaml
grep -q 'runAsNonRoot: true' /tmp/pgqn-helm-ci.yaml
grep -q 'kind: NetworkPolicy' /tmp/pgqn-helm-ci.yaml
grep -q 'type: RuntimeDefault' /tmp/pgqn-helm-ci.yaml
# Must not emit plaintext API key env.
if grep -q 'SECURITY_API_KEY:' /tmp/pgqn-helm-ci.yaml; then
  echo "ERROR: rendered chart still sets SECURITY_API_KEY plaintext" >&2
  exit 1
fi
echo "    ok: rendered $(wc -l </tmp/pgqn-helm-ci.yaml) lines"

echo "==> schedule runner without allowlist must fail"
if helm template pgqn-strict-check "${CHART}" -f "${CHART}/ci-values.yaml" \
  --set security.scheduleRunnerEnabled=true >/tmp/pgqn-helm-sched.yaml 2>/tmp/pgqn-helm-sched.err; then
  echo "ERROR: schedule runner without webhook allowlist should fail" >&2
  exit 1
fi
echo "    ok: $(tr '\n' ' ' </tmp/pgqn-helm-sched.err | head -c 200)"

echo "helm StrictMode checks passed"
