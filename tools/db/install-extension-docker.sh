#!/bin/sh
# Copy extension into container's sharedir so CREATE EXTENSION pgquerynarrative works.
# Run from repo root. Postgres must be running (docker compose up -d postgres).
set -e

ROOT_DIR="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT_DIR"

if ! docker compose exec -T postgres pg_isready -U postgres >/dev/null 2>&1; then
  echo "Postgres container not running. Start with: docker compose up -d postgres"
  exit 1
fi

# Mounted at /extension; copy to Postgres extension dir
docker compose exec -T postgres sh -c 'cp /extension/pgquerynarrative.control /extension/pgquerynarrative--*.sql "$(pg_config --sharedir)/extension/"'
echo "Extension files installed in container."
echo "In psql: CREATE EXTENSION pgquerynarrative;"
