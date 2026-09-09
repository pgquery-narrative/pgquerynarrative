#!/bin/sh
set -e

DB_HOST="${DATABASE_HOST:-postgres}"
DB_PORT="${DATABASE_PORT:-5432}"
DB_NAME="${DATABASE_NAME:-pgquerynarrative}"
DB_USER="${DATABASE_USER:-pgquerynarrative_app}"
DB_PASSWORD="${DATABASE_PASSWORD:-pgquerynarrative_app}"

export PGPASSWORD="${DB_PASSWORD}"

attempts=30
while [ $attempts -gt 0 ]; do
  if pg_isready -h "${DB_HOST}" -p "${DB_PORT}" -U "${DB_USER}" >/dev/null 2>&1; then
    break
  fi
  attempts=$((attempts - 1))
  sleep 1
done

if [ $attempts -eq 0 ]; then
  echo "Postgres is not ready at ${DB_HOST}:${DB_PORT}"
  exit 1
fi

export DB_URL="postgres://${DB_USER}:${DB_PASSWORD}@${DB_HOST}:${DB_PORT}/${DB_NAME}?sslmode=${DATABASE_SSL_MODE:-disable}"

# Migrations are DDL: they create extensions (pg_stat_statements, pgvector,
# hypopg) and ALTER ROLE, none of which the runtime application role is allowed
# to do — by design, since that role also executes user SQL. Running them as
# DATABASE_USER made a fresh deploy of this image fail at migration 000019 with
# "permission denied to create extension".
#
# Use DATABASE_MIGRATION_USER / DATABASE_MIGRATION_PASSWORD (or a full
# DATABASE_MIGRATION_URL) for a role that may create extensions and alter roles.
# Falling back to DATABASE_USER keeps existing deployments working, where the
# schema is already at the required version and `up` is a no-op.
# Mirror of config.StrictMode() in app/config/validate.go. Keep the two in step:
# APP_ENV of "production"/"prod" (any case), or SECURITY_STRICT parsing as true
# the way Go's strconv.ParseBool accepts it.
is_strict_mode() {
  _env="$(printf '%s' "${APP_ENV:-}" | tr '[:upper:]' '[:lower:]' | tr -d '[:space:]')"
  if [ "$_env" = "production" ] || [ "$_env" = "prod" ]; then
    return 0
  fi
  case "${SECURITY_STRICT:-}" in
    1|t|T|true|TRUE|True) return 0 ;;
  esac
  return 1
}

MIGRATE_USER="${DATABASE_MIGRATION_USER:-$DB_USER}"
MIGRATE_PASSWORD="${DATABASE_MIGRATION_PASSWORD:-$DB_PASSWORD}"
MIGRATE_URL="${DATABASE_MIGRATION_URL:-postgres://${MIGRATE_USER}:${MIGRATE_PASSWORD}@${DB_HOST}:${DB_PORT}/${DB_NAME}?sslmode=${DATABASE_SSL_MODE:-disable}}"

if [ "${PGQUERYNARRATIVE_SKIP_MIGRATIONS:-false}" != "true" ] \
   && [ "${MIGRATE_USER}" = "${DB_USER}" ] && [ -z "${DATABASE_MIGRATION_URL:-}" ]; then
  # In production, fail closed rather than starting a migration the runtime role
  # cannot finish. Attempting it leaves the schema half-applied and the failure
  # surfaces as an opaque mid-migration permission error; refusing to start says
  # exactly what is missing.
  #
  # This must agree with config.StrictMode() (app/config/validate.go), which is
  # case-insensitive, accepts "prod" as well as "production", and also honours
  # SECURITY_STRICT. Matching only APP_ENV=production exactly would leave every
  # other strict deployment on the old warn-and-continue path.
  if is_strict_mode; then
    echo "Configuration error: migrations are enabled but no migration credential is set." >&2
    echo "Set DATABASE_MIGRATION_USER/DATABASE_MIGRATION_PASSWORD (or DATABASE_MIGRATION_URL) to a" >&2
    echo "role that may CREATE EXTENSION and ALTER ROLE, or run migrations as a separate Job and" >&2
    echo "start this container with PGQUERYNARRATIVE_SKIP_MIGRATIONS=true." >&2
    exit 1
  fi
  echo "Running migrations as ${DB_USER}. If this is a fresh database, set" >&2
  echo "DATABASE_MIGRATION_USER to a role that may create extensions and alter roles." >&2
fi

if [ "${PGQUERYNARRATIVE_SKIP_MIGRATIONS:-false}" = "true" ]; then
  echo "PGQUERYNARRATIVE_SKIP_MIGRATIONS=true — not running migrations." >&2
else
  /app/bin/migrate -path /app/app/db/migrations -database "${MIGRATE_URL}" up
fi

if [ "${PGQUERYNARRATIVE_SEED:-false}" = "true" ]; then
  psql "${DB_URL}" -f /app/tools/db/seed.sql
fi

# Drop every credential the server does not need before handing over to it.
#
# The migration role may CREATE EXTENSION and ALTER ROLE; the runtime role
# deliberately may not, because the runtime role also executes user-supplied SQL.
# Leaving the migration credentials in the environment would hand a compromised
# server process exactly the DDL privileges this split exists to withhold.
#
# The stronger deployment shape is still to run migrations in a separate Job or
# init container so the runtime container never receives these at all — see
# deploy/ — but a single-container deployment should not pay for that with a
# privileged secret sitting in the server's environment for the life of the pod.
unset DATABASE_MIGRATION_USER DATABASE_MIGRATION_PASSWORD DATABASE_MIGRATION_URL
unset MIGRATE_USER MIGRATE_PASSWORD MIGRATE_URL
unset PGPASSWORD DB_URL

exec /app/bin/server
