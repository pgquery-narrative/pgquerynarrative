#!/usr/bin/env sh
set -eu

DB_URL="${1:-${DB_URL:-postgres://postgres:postgres@localhost:5432/pgquerynarrative?sslmode=disable}}"
READONLY_URL="${READONLY_DB_URL:-postgres://pgquerynarrative_readonly:pgquerynarrative_readonly@localhost:5432/pgquerynarrative?sslmode=disable}"
APP_ROLE="${APP_ROLE:-pgquerynarrative_app}"
READONLY_ROLE="${READONLY_ROLE:-pgquerynarrative_readonly}"

echo "== Verifying PostgreSQL security boundary =="

role_sql="
SELECT rolname, rolsuper, rolcreatedb, rolcreaterole, rolreplication, rolbypassrls, rolinherit
FROM pg_roles
WHERE rolname IN ('${APP_ROLE}', '${READONLY_ROLE}')
ORDER BY rolname;
"

psql "$DB_URL" -v ON_ERROR_STOP=1 -c "$role_sql"

bad_roles="$(psql "$DB_URL" -At -v ON_ERROR_STOP=1 -c "
SELECT rolname
FROM pg_roles
WHERE rolname IN ('${APP_ROLE}', '${READONLY_ROLE}')
  AND (rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls);
")"
if [ -n "$bad_roles" ]; then
  echo "ERROR: privileged database roles found: $bad_roles" >&2
  exit 1
fi

if psql "$READONLY_URL" -v ON_ERROR_STOP=1 -c "INSERT INTO demo.sales DEFAULT VALUES;" >/tmp/pgqn-readonly-write.log 2>&1; then
  echo "ERROR: readonly role was able to write to demo.sales" >&2
  exit 1
fi

if psql "$READONLY_URL" -v ON_ERROR_STOP=1 -c "CREATE TABLE demo.pgqn_forbidden_write(id int);" >/tmp/pgqn-readonly-ddl.log 2>&1; then
  echo "ERROR: readonly role was able to create a table" >&2
  exit 1
fi

if psql "$READONLY_URL" -v ON_ERROR_STOP=1 -c "SELECT 1 FROM pg_catalog.pg_authid LIMIT 1;" >/tmp/pgqn-readonly-catalog.log 2>&1; then
  echo "ERROR: readonly role was able to read blocked system catalog secrets" >&2
  exit 1
fi

echo "OK: readonly role cannot write, create, bypass RLS, or read blocked public objects."
