#!/usr/bin/env bash
# Verify the pqn PostgreSQL image (tools/docker/postgres-pqn.Dockerfile) the way a user meets it:
#   1. pull-and-run: a new container has pqn created, initialized and ready, with pg_stat_statements
#      preloaded, under the user's own superuser name and database
#   2. it survives a restart, and a person can be enrolled and investigate through it
#   3. an existing data volume made by the plain image keeps working under this image: the files are
#      in the image, so CREATE EXTENSION pqn is all the user runs
#
# Requires: docker. Usage: bash tools/db/verify-pqn-image.sh [base image]     (default postgres:18)
#   bash tools/db/verify-pqn-image.sh postgres:16
#   bash tools/db/verify-pqn-image.sh postgres:18-alpine
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT_DIR"
BASE="${1:-postgres:18}"
TAG="pqn-image-verify:$$"
ID="$$"
C1="pqn-image-fresh-$ID"; C2="pqn-image-vol-$ID"; V="pqn-image-vol-$ID"
PASS=0; FAIL=0
ok()  { PASS=$((PASS + 1)); echo "  PASS  $1"; }
bad() { FAIL=$((FAIL + 1)); echo "  FAIL  $1"; }
cleanup() { docker rm -f "$C1" "$C2" "${C2}-plain" >/dev/null 2>&1 || true; docker volume rm "$V" >/dev/null 2>&1 || true; docker rmi "$TAG" >/dev/null 2>&1 || true; }
trap cleanup EXIT INT TERM
q() { local c="$1" u="$2" d="$3"; shift 3; docker exec -i "$c" psql -X -q -At -U "$u" -d "$d" "$@"; }
ready() { local i=0; until docker exec "$1" pg_isready -U "$2" -h 127.0.0.1 -d "$3" >/dev/null 2>&1; do i=$((i + 1)); [ "$i" -gt 90 ] && { echo "$1 did not start" >&2; return 1; }; sleep 1; done; sleep 2; }
expect() { if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 -> got '$3', wanted '$2'"; fi; }
# PostgreSQL 18 images keep data in /var/lib/postgresql, earlier ones in /var/lib/postgresql/data.
case "$BASE" in postgres:18*) MOUNT=/var/lib/postgresql ;; *) MOUNT=/var/lib/postgresql/data ;; esac

echo "== Build the image on $BASE"
docker build -q -f tools/docker/postgres-pqn.Dockerfile --build-arg "POSTGRES_IMAGE=$BASE" -t "$TAG" . >/dev/null && ok "the image builds" || { bad "the image does not build"; exit 1; }

echo "== 1. Pull and run: a new container is ready with nothing more to do"
docker run -d --name "$C1" -e POSTGRES_USER=team -e POSTGRES_PASSWORD=secret -e POSTGRES_DB=app "$TAG" >/dev/null
ready "$C1" team app
expect "pqn is created, at 1.1" "pqn 1.1" "$(q "$C1" team app -c "SELECT extname || ' ' || extversion FROM pg_extension WHERE extname = 'pqn'")"
expect "the ledger is initialized" "2" "$(q "$C1" team app -c "SELECT count(*) FROM pqn_ledger.schema_migrations")"
expect "pg_stat_statements is preloaded" "pg_stat_statements" "$(q "$C1" team app -c "SHOW shared_preload_libraries")"
expect "the roles exist" "7" "$(q "$C1" team app -c "SELECT count(*) FROM pg_roles WHERE rolname IN ('pqn_owner','pqn_reader','pqn_stats','pqn_ledger','pqn_viewer','pqn_analyst','pqn_admin')")"
expect "the setup check finds nothing blocking" "0" "$(q "$C1" team app -c "SELECT count(*) FROM pqn_api.verify_setup() WHERE level = 'BLOCK'")"
expect "the user's own superuser name works, no postgres role is needed" "t" "$(q "$C1" team app -c "SELECT rolsuper FROM pg_roles WHERE rolname = 'team'")"

echo "== 2. It works through the image, and after a restart"
q "$C1" team app -c "CREATE TABLE t AS SELECT g AS id, g % 10 AS k FROM generate_series(1, 100000) g" \
  -c "SELECT pqn_api.expose('t', ARRAY['id','k']) IS NOT NULL" \
  -c "CREATE ROLE alice LOGIN" -c "SELECT pqn_api.enroll('alice', 'analyst') IS NOT NULL" >/dev/null
docker restart "$C1" >/dev/null; ready "$C1" team app
expect "an analyst can run a query" "100000" "$(q "$C1" alice app -c "SELECT (pqn_api.run('SELECT count(*) AS n FROM t')->'rows'->0->>'n')")"
expect "pqn top works (statistics are recorded)" "t" "$(q "$C1" team app -c "SELECT count(*) > 0 FROM pqn_api.top(5)")"
expect "an analyst can investigate and the result is recorded" "1" "$(q "$C1" alice app -c "SELECT pqn_api.investigate('SELECT * FROM t WHERE k = 3', 'from the image')->>'investigation_id'")"
expect "the extension is still there after a restart" "pqn 1.1" "$(q "$C1" team app -c "SELECT extname || ' ' || extversion FROM pg_extension WHERE extname = 'pqn'")"

echo "== 3. An existing data volume: the user swaps the image and creates the extension"
docker volume create "$V" >/dev/null
docker run -d --name "${C2}-plain" -v "$V:$MOUNT" -e POSTGRES_PASSWORD=secret -e POSTGRES_DB=shop "$BASE" >/dev/null
ready "${C2}-plain" postgres shop
q "${C2}-plain" postgres shop -c "CREATE TABLE orders AS SELECT g AS id FROM generate_series(1, 5000) g" >/dev/null
docker stop -t 30 "${C2}-plain" >/dev/null; docker rm "${C2}-plain" >/dev/null
docker run -d --name "$C2" -v "$V:$MOUNT" -e POSTGRES_PASSWORD=secret "$TAG" >/dev/null
ready "$C2" postgres shop
expect "the user's data is untouched" "5000" "$(q "$C2" postgres shop -c "SELECT count(*) FROM orders")"
expect "the init script did not run on existing data, so nothing is created yet" "0" "$(q "$C2" postgres shop -c "SELECT count(*) FROM pg_extension WHERE extname = 'pqn'")"
expect "but the extension is available, from the image" "pqn 1.1" "$(q "$C2" postgres shop -c "SELECT name || ' ' || default_version FROM pg_available_extensions WHERE name = 'pqn'")"
q "$C2" postgres shop -c "CREATE EXTENSION pqn" >/dev/null && q "$C2" postgres shop -At -c "SELECT pqn_api.init()" >/dev/null
expect "CREATE EXTENSION pqn and init() are all that is needed" "0" "$(q "$C2" postgres shop -c "SELECT count(*) FROM pqn_api.verify_setup() WHERE level = 'BLOCK'")"
expect "pg_stat_statements is not preloaded on old data (its postgresql.conf is the user's)" "" "$(q "$C2" postgres shop -c "SHOW shared_preload_libraries")"
TOP_OUT="$(q "$C2" postgres shop -c "SELECT count(*) FROM pqn_api.top(3)" 2>&1 || true)"
case "$TOP_OUT" in *"pg_stat_statements is not installed"*) ok "pqn top says so clearly, and everything else works" ;; *) bad "pqn top did not give the clear message: $TOP_OUT" ;; esac

echo
echo "RESULT pqn image ($BASE): pass=$PASS fail=$FAIL"
[ "$FAIL" -eq 0 ]
