#!/bin/sh
# Copy extension files to Postgres sharedir so CREATE EXTENSION pgquerynarrative works.
# Run from repo root. Requires: pg_config in PATH (or set PG_CONFIG).
set -e

ROOT_DIR="$(cd "$(dirname "$0")/../.." && pwd)"
PG_CONFIG="${PG_CONFIG:-pg_config}"
EXT_DIR="$ROOT_DIR/infra/postgres-extension"
DEST="$($PG_CONFIG --sharedir)/extension"

cp "$EXT_DIR/pgquerynarrative.control" "$DEST/"
cp "$EXT_DIR"/pgquerynarrative--*.sql "$DEST/"
echo "Extension files copied to $DEST"
echo "In psql: CREATE EXTENSION pgquerynarrative;"
