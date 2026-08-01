#!/usr/bin/env bash
# Capture a README demo GIF from a running PgQueryNarrative instance.
# Prerequisites:
#   - App running with 10M-row demo.sales (make demo-bootstrap or make seed-large-docker)
#   - SECURITY_EXPLAIN_ANALYZE_ENABLED=true (compose default for local demo)
#   - Node/npm, Playwright browsers, ffmpeg
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"
BASE_URL="${BASE_URL:-http://127.0.0.1:8080}"
OUT_DIR="$ROOT/docs/assets"
OUT_GIF="$OUT_DIR/demo-workflow.gif"
MIN_ROWS="${DEMO_GIF_MIN_ROWS:-1000000}"

echo "==> Checking app at $BASE_URL"
curl -fsS "$BASE_URL/ready" >/dev/null || {
  echo "ERROR: app not ready. Run: make demo-bootstrap   # (SEED_LARGE=1, ~10M rows)" >&2
  exit 1
}

echo "==> Verifying credible demo.sales scale (need >= $MIN_ROWS rows)"
ROW_COUNT="$(
  docker compose exec -T postgres psql -U postgres -d "${POSTGRES_DB:-pgquerynarrative}" -tAc \
    "SELECT count(*) FROM demo.sales" 2>/dev/null | tr -d '[:space:]' || true
)"
if [[ -z "$ROW_COUNT" || ! "$ROW_COUNT" =~ ^[0-9]+$ ]]; then
  echo "ERROR: could not count demo.sales rows via docker compose postgres." >&2
  exit 1
fi
if (( ROW_COUNT < MIN_ROWS )); then
  echo "ERROR: demo.sales has only $ROW_COUNT rows (need >= $MIN_ROWS for a credible README GIF)." >&2
  echo "       Run: make seed-large-docker   # or: make demo-bootstrap" >&2
  exit 1
fi
echo "    demo.sales rows=$ROW_COUNT"

echo "==> Running Playwright demo walkthrough (records video)"
cd "$ROOT/test/playwright"
if [[ ! -d node_modules/@playwright/test ]]; then
  npm install @playwright/test --no-save 2>/dev/null || true
fi
rm -rf test-results
PLAYWRIGHT_BASE_URL="$BASE_URL" DEMO_CAPTURE=1 npx playwright test demo-workflow.spec.ts --config playwright.config.ts

VIDEO="$(find test-results -name 'video.webm' 2>/dev/null | head -1 || true)"
if [[ -z "$VIDEO" ]]; then
  echo "ERROR: No Playwright video captured." >&2
  exit 1
fi

if ! command -v ffmpeg >/dev/null 2>&1; then
  echo "ERROR: ffmpeg not found (brew install ffmpeg)." >&2
  exit 1
fi

echo "==> Converting video to GIF with ffmpeg"
ffmpeg -y -i "$VIDEO" -vf "fps=12,scale=1280:-1:flags=lanczos,split[s0][s1];[s0]palettegen=max_colors=256:stats_mode=diff[p];[s1][p]paletteuse=dither=bayer:bayer_scale=3" "$OUT_GIF"
# Static PNG fallback (late frame)
ffmpeg -y -i "$OUT_GIF" -vf "select=eq(n\\,60)" -vframes 1 "$OUT_DIR/demo-workflow.png" >/dev/null 2>&1 || true
echo "✅ Wrote $OUT_GIF"
