#!/usr/bin/env bash
# Emits a GA / pilot sign-off report from pilot acceptance results and environment metadata.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

OUT="${1:-}"
TS="$(date -u +"%Y-%m-%dT%H:%M:%SZ")"
ENV_NAME="${PILOT_ENV_NAME:-staging}"
VERSION="$(git describe --tags --always 2>/dev/null || echo unknown)"

emit() {
  if [[ -n "$OUT" ]]; then
    tee -a "$OUT"
  else
    cat
  fi
}

{
  echo "# PgQueryNarrative pilot / GA sign-off report"
  echo ""
  echo "- **Environment:** ${ENV_NAME}"
  echo "- **Generated:** ${TS}"
  echo "- **Version:** ${VERSION}"
  echo ""
  echo "## Automated gates"
  echo ""
  echo "| Gate | Command | Status |"
  echo "|------|---------|--------|"
  echo "| Pilot acceptance | \`make pilot-acceptance\` | run and attach log |"
  echo "| DB security verify | \`make db-security-verify-docker\` | run and attach log |"
  echo "| Unit + integration | \`make test\` | run and attach log |"
  echo "| Browser E2E | \`make test-playwright\` | run and attach log |"
  echo "| Load smoke | \`make test-load-smoke\` | run and attach log |"
  echo ""
  echo "## Manual sign-off (required before GA)"
  echo ""
  echo "- [ ] Architecture review completed"
  echo "- [ ] Security review completed (auth, RLS, LLM governance, webhooks)"
  echo "- [ ] Corporate IdP browser login verified on staging (\`/auth/login\` → callback → \`/auth/session\`)"
  echo "- [ ] Backup → restore drill evidence attached (from \`make pilot-acceptance\`)"
  echo "- [ ] On-call runbooks reviewed (\`docs/reference/operations.md\`)"
  echo "- [ ] Credential rotation procedure exercised or scheduled"
  echo ""
  echo "## Pilot KPI snapshot (fill after 7-day window)"
  echo ""
  echo "| Metric | Target | Observed |"
  echo "|--------|--------|----------|"
  echo "| Auth success rate | ≥ 99% | |"
  echo "| Query p95 latency | < QUERY_TIMEOUT | |"
  echo "| LLM budget denials | near zero | |"
  echo "| Scheduler duplicate deliveries | 0 | |"
  echo "| Cross-org IDOR findings | 0 | |"
  echo ""
  echo "## Approvals"
  echo ""
  echo "| Role | Name | Date |"
  echo "|------|------|------|"
  echo "| Engineering lead | | |"
  echo "| Security | | |"
  echo "| Data platform | | |"
} | emit

if [[ -n "$OUT" ]]; then
  echo "Report written to $OUT"
fi
