#!/usr/bin/env bash
# Run `npm audit --audit-level=high`, but do not fail CI when npmjs.org's audit
# endpoint is unreachable (503 / timeout / connection reset). A real
# high-severity advisory still fails the job.
set -uo pipefail

out="$(npm audit --audit-level=high 2>&1)"
status=$?
echo "$out"

if [ "$status" -eq 0 ]; then
  exit 0
fi

if echo "$out" | grep -qiE 'Service Unavailable|ECONNRESET|ETIMEDOUT|ENOTFOUND|EAI_AGAIN|socket hang up|502 Bad Gateway|503|network|audit endpoint returned an error'; then
  echo "::warning::npm audit skipped — npmjs.org audit endpoint is unavailable."
  exit 0
fi

exit "$status"
