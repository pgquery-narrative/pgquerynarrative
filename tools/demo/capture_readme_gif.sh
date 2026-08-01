#!/usr/bin/env bash
# Capture a compact README demo GIF from a running PgQueryNarrative instance.
# Prerequisites:
#   - App running with 10M-row demo.sales (make demo-bootstrap or make seed-large-docker)
#   - SECURITY_EXPLAIN_ANALYZE_ENABLED=true (compose default for local demo)
#   - Node/npm, Playwright browsers, ffmpeg
#
# Capture script deep-links the investigation, zooms key evidence, uses top captions,
# and encodes a smaller GIF suitable for GitHub README load times.
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

echo "==> Running tight Playwright walkthrough (zoom + top captions)"
cd "$ROOT/test/playwright"
if [[ ! -d node_modules/@playwright/test ]]; then
  npm install @playwright/test --no-save 2>/dev/null || true
fi
rm -rf test-results
export PLAYWRIGHT_BROWSERS_PATH="${PLAYWRIGHT_BROWSERS_PATH:-$HOME/Library/Caches/ms-playwright}"
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

echo "==> Encoding compact README GIF (720px @ 6fps, mild speed-up)"
# README-friendly: slight speed-up + lower fps/width + smaller palette.
ffmpeg -y -i "$VIDEO" \
  -filter_complex "[0:v]setpts=0.82*PTS,fps=6,scale=720:-1:flags=lanczos,split[s0][s1];[s0]palettegen=max_colors=128:stats_mode=diff[p];[s1][p]paletteuse=dither=bayer:bayer_scale=5" \
  "$OUT_GIF"

FRAME_N="$(ffprobe -v error -count_frames -select_streams v:0 -show_entries stream=nb_read_frames -of csv=p=0 "$OUT_GIF" 2>/dev/null || echo 60)"
if [[ "$FRAME_N" =~ ^[0-9]+$ ]] && (( FRAME_N > 12 )); then
  PICK=$(( FRAME_N * 2 / 3 ))
else
  PICK=40
fi
ffmpeg -y -i "$OUT_GIF" -vf "select=eq(n\\,${PICK})" -vframes 1 "$OUT_DIR/demo-workflow.png" >/dev/null 2>&1 || true

DUR="$(ffprobe -v error -show_entries format=duration -of default=noprint_wrappers=1:nokey=1 "$OUT_GIF" 2>/dev/null || true)"
SIZE="$(wc -c < "$OUT_GIF" | tr -d ' ')"
echo "✅ Wrote $OUT_GIF (duration=${DUR:-?}s size=${SIZE} bytes)"
if [[ "$SIZE" =~ ^[0-9]+$ ]] && (( SIZE > 3500000 )); then
  echo "WARN: GIF is still >3.5MB; tighten dwells or lower fps further." >&2
fi
