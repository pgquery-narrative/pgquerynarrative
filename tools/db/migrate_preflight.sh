#!/bin/sh
# Pre-flight check for "migrate up" against the Compose Postgres.
#
# golang-migrate trusts schema_migrations.version: it applies only versions
# greater than the recorded one, and it never verifies that the schema actually
# matches. Two states therefore produce failures that point at the wrong thing:
#
#   1. dirty = true — a previous "up" failed partway. migrate then refuses every
#      subsequent run with "Dirty database version N. Fix and force version.",
#      which is a dead end unless you know golang-migrate's internals.
#   2. A volume left over from an older checkout whose recorded version outran
#      the DDL of the current migration set. migrate skips the migrations that
#      would have created the missing objects, and a much later migration dies
#      on something unrelated ("column ... does not exist").
#
# Case 1 is detectable up front and is what this script reports. Case 2 is
# reported by migrate-fail-hint.sh after the failure.
set -eu

DB_NAME="${DB_NAME:-pgquerynarrative}"

q() {
	docker compose exec -T postgres psql -U postgres -d "$DB_NAME" -tAc "$1" 2>/dev/null | tr -d '[:space:]' || true
}

# No schema_migrations yet means a fresh database: nothing to guard.
[ "$(q "SELECT to_regclass('public.schema_migrations') IS NOT NULL")" = "t" ] || exit 0

dirty="$(q "SELECT dirty FROM public.schema_migrations LIMIT 1")"
version="$(q "SELECT version FROM public.schema_migrations LIMIT 1")"
[ -n "$version" ] || exit 0
[ "$dirty" = "t" ] || exit 0

prev=$((version - 1))
cat >&2 <<MSG

❌ Database "$DB_NAME" is in a dirty migration state at version $version.

   A previous migration failed partway through. golang-migrate does not roll
   back, and it will refuse to run again until the flag is cleared.

   If this is a local database you can discard (the usual case in dev), the
   clean fix is to start from an empty volume:

       docker compose down -v && make start-docker

   To keep the data, repair whatever migration $version left half-applied, then
   record the version the schema actually matches and re-run:

       make migrate-force-docker VERSION=$prev
       make migrate-docker

   $prev is right when migration $version itself failed. If this database came from
   an older checkout, the schema may be older than the recorded version — force
   to the last version whose objects are really present, or migrate will skip
   the ones in between and fail again further along.

MSG
exit 1
