#!/bin/sh
# Printed when "migrate up" fails, to name the cause the raw error hides.
#
# The failing SQL that migrate dumps is usually not the broken migration: when a
# stale volume records a version whose DDL never ran in this migration set,
# migrate skips those versions and a much later migration is the first one to
# reference the missing object.
set -eu

DB_NAME="${DB_NAME:-pgquerynarrative}"
version="$(docker compose exec -T postgres psql -U postgres -d "$DB_NAME" -tAc \
	"SELECT version FROM public.schema_migrations LIMIT 1" 2>/dev/null | tr -d '[:space:]' || true)"

cat >&2 <<MSG

❌ Migrations failed${version:+ (schema_migrations is now at version $version, dirty)}.

   Read the error above, then check the most common cause first: a Postgres
   volume left over from an older checkout. golang-migrate only applies versions
   greater than the one recorded in schema_migrations and never verifies the
   schema, so an outdated recorded version makes it skip the migrations that
   create the objects a later one needs. The error then names an innocent
   migration and a column or table that some *earlier* migration should have
   created.

   Confirm by comparing the recorded version with the schema:

       docker compose exec postgres psql -U postgres -d $DB_NAME \\
         -c 'SELECT version, dirty FROM public.schema_migrations'

   For a local database you can discard, start from an empty volume:

       docker compose down -v && make start-docker

MSG
exit 1
