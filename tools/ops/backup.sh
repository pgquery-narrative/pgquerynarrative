#!/usr/bin/env bash
# Logical backup helper for PgQueryNarrative Postgres (app schema + demo data).
# Requires pg_dump and a reachable DATABASE_* connection (or docker compose postgres).
#
# Usage:
#   tools/ops/backup.sh [output-file]
#   DATABASE_HOST=localhost DATABASE_USER=postgres DATABASE_PASSWORD=postgres tools/ops/backup.sh /tmp/pgqn.dump
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

OUT="${1:-pgquerynarrative-$(date -u +%Y%m%dT%H%M%SZ).sql.gz}"
HOST="${DATABASE_HOST:-localhost}"
PORT="${DATABASE_PORT:-5432}"
DB="${DATABASE_NAME:-pgquerynarrative}"
USER="${DATABASE_USER:-postgres}"
export PGPASSWORD="${DATABASE_PASSWORD:-postgres}"

echo "Backing up ${DB}@${HOST}:${PORT} -> ${OUT}"
pg_dump -h "$HOST" -p "$PORT" -U "$USER" -d "$DB" \
  --no-owner --no-acl \
  --schema=app --schema=demo --schema=opendata \
  | gzip -c > "$OUT"
echo "Backup complete: ${OUT} ($(wc -c < "$OUT" | tr -d ' ') bytes compressed)"
