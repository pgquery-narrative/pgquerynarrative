#!/usr/bin/env bash
# Logical backup helper for PgQueryNarrative Postgres (app schema + demo data).
# Prefers host pg_dump when available; falls back to docker compose postgres.
#
# Usage:
#   tools/ops/backup.sh [output-file]
#   DATABASE_HOST=localhost DATABASE_USER=postgres DATABASE_PASSWORD=postgres tools/ops/backup.sh /tmp/pgqn.dump
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

OUT="${1:-pgquerynarrative-$(date -u +"%Y%m%dT%H%M%SZ").sql.gz}"
HOST="${DATABASE_HOST:-localhost}"
PORT="${DATABASE_PORT:-5432}"
DB="${DATABASE_NAME:-pgquerynarrative}"
USER="${DATABASE_USER:-postgres}"
export PGPASSWORD="${DATABASE_PASSWORD:-postgres}"

dump_via_host() {
  pg_dump -h "$HOST" -p "$PORT" -U "$USER" -d "$DB" \
    --no-owner --no-acl \
    --schema=app --schema=demo --schema=opendata
  pg_dump -h "$HOST" -p "$PORT" -U "$USER" -d "$DB" \
    --no-owner --no-acl \
    --table=public.schema_migrations
}

dump_via_docker() {
  docker compose exec -T postgres pg_dump -U "$USER" -d "$DB" \
    --no-owner --no-acl \
    --schema=app --schema=demo --schema=opendata
  docker compose exec -T postgres pg_dump -U "$USER" -d "$DB" \
    --no-owner --no-acl \
    --table=public.schema_migrations
}

echo "Backing up ${DB}@${HOST}:${PORT} -> ${OUT}"
if command -v pg_dump >/dev/null 2>&1 && dump_via_host 2>/dev/null | gzip -c > "$OUT" && [[ -s "$OUT" ]]; then
  :
elif dump_via_docker | gzip -c > "$OUT" && [[ -s "$OUT" ]]; then
  echo "  (used docker compose postgres pg_dump)"
else
  echo "Backup failed: host pg_dump and docker compose pg_dump both unavailable" >&2
  rm -f "$OUT"
  exit 1
fi
echo "Backup complete: ${OUT} ($(wc -c < "$OUT" | tr -d ' ') bytes compressed)"
