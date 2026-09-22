#!/bin/sh
# First-start setup for the pqn image: create pg_stat_statements and pqn in $POSTGRES_DB.
set -e
psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" \
  -c "CREATE EXTENSION IF NOT EXISTS pg_stat_statements" \
  -c "CREATE EXTENSION pqn" \
  -c "SELECT pqn_api.init()"
