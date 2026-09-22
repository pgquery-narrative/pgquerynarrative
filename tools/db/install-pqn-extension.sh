#!/bin/sh
# Copy the "pqn" extension files to the local Postgres sharedir so CREATE EXTENSION pqn works.
# Run it from a clone of the repository (make install-pqn-extension), or as pqn-extension/install.sh
# inside a release archive. Requires pg_config in PATH (or set PG_CONFIG).
# Then, as a superuser:  CREATE EXTENSION pqn;  SELECT pqn_api.init();
set -e

HERE="$(cd "$(dirname "$0")" && pwd)"
if [ -f "$HERE/pqn.control" ]; then
  EXT_DIR="$HERE"                                   # release archive: the files sit next to this script
else
  EXT_DIR="$(cd "$HERE/../.." && pwd)/infra/pqn-extension"   # a clone of the repository
fi
PG_CONFIG="${PG_CONFIG:-pg_config}"
DEST="$($PG_CONFIG --sharedir)/extension"

cp "$EXT_DIR/pqn.control" "$EXT_DIR"/pqn--*.sql "$DEST/"
echo "pqn extension files copied to $DEST"
echo "1. As a superuser:  CREATE EXTENSION pqn;  SELECT pqn_api.init();"
echo "2. Without a superuser, a DBA first runs: psql -d yourdb -v installer=<role> -f $EXT_DIR/pqn-roles.sql"
