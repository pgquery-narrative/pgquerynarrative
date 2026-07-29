#!/usr/bin/env bash
# Capture a README demo GIF from a running PgQueryNarrative instance.
# Prerequisites: app running (make demo), Node/npm, Playwright browsers, ffmpeg (optional).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"
BASE_URL="${BASE_URL:-http://127.0.0.1:8080}"
OUT_DIR="$ROOT/docs/assets"
OUT_GIF="$OUT_DIR/demo-workflow.gif"

echo "==> Checking app at $BASE_URL"
curl -fsS "$BASE_URL/ready" >/dev/null || {
  echo "ERROR: app not ready. Run: make demo" >&2
  exit 1
}

echo "==> Running Playwright demo walkthrough (records video)"
cd "$ROOT/test/playwright"
if [[ ! -d node_modules/@playwright/test ]]; then
  npm install @playwright/test --no-save 2>/dev/null || true
fi
PLAYWRIGHT_BASE_URL="$BASE_URL" DEMO_CAPTURE=1 npx playwright test demo-workflow.spec.ts --config playwright.config.ts || true

VIDEO="$(find test-results -name 'video.webm' 2>/dev/null | head -1 || true)"
if [[ -z "$VIDEO" ]]; then
  echo "WARN: No Playwright video captured. Use docs/assets/demo-workflow.svg in README."
  exit 0
fi

if command -v ffmpeg >/dev/null 2>&1; then
  echo "==> Converting video to GIF with ffmpeg"
  ffmpeg -y -i "$VIDEO" -vf "fps=10,scale=960:-1:flags=lanczos,split[s0][s1];[s0]palettegen[p];[s1][p]paletteuse" "$OUT_GIF"
  echo "✅ Wrote $OUT_GIF"
else
  echo "WARN: ffmpeg not found; copy $VIDEO manually or install ffmpeg"
fi
