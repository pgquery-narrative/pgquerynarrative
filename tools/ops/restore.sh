#!/usr/bin/env bash
# Restore a logical backup produced by tools/ops/backup.sh into an existing database.
#
# Usage:
#   tools/ops/restore.sh backup.sql.gz
#   DATABASE_NAME=pgqn_drill tools/ops/restore.sh /tmp/pgqn.dump
set -euo pipefail

if [[ $# -lt 1 ]]; then
  echo "usage: $0 <backup.sql.gz>" >&2
  exit 1
fi

BACKUP="$1"
HOST="${DATABASE_HOST:-localhost}"
PORT="${DATABASE_PORT:-5432}"
DB="${DATABASE_NAME:-pgquerynarrative}"
USER="${DATABASE_USER:-postgres}"
export PGPASSWORD="${DATABASE_PASSWORD:-postgres}"

if [[ ! -f "$BACKUP" ]]; then
  echo "backup file not found: $BACKUP" >&2
  exit 1
fi

echo "Restoring ${BACKUP} -> ${DB}@${HOST}:${PORT}"
gunzip -c "$BACKUP" | psql -h "$HOST" -p "$PORT" -U "$USER" -d "$DB" -v ON_ERROR_STOP=1
echo "Restore complete."
