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

if psql "$READONLY_URL" -v ON_ERROR_STOP=1 -c "SELECT 1 FROM app.saved_queries LIMIT 1;" >/tmp/pgqn-readonly-app.log 2>&1; then
  echo "ERROR: readonly role was able to read app.saved_queries (app schema must be blocked)" >&2
  exit 1
fi

app_usage="$(psql "$READONLY_URL" -At -v ON_ERROR_STOP=1 -c "SELECT has_schema_privilege(current_user, 'app', 'USAGE');")"
if [ "$app_usage" = "t" ]; then
  echo "ERROR: readonly role has USAGE on app schema" >&2
  exit 1
fi

# --- hypopg boundary -------------------------------------------------------
#
# ProjectIndexCost has to lift transaction_read_only to let hypopg register a
# hypothetical index (hypopg refuses inside a read-only transaction). That makes
# the read-only *flag* an unreliable safety net, so this block proves the real
# one: with the flag off, the analytical role must still be unable to write or
# run DDL, because it lacks the privilege. If that ever stops holding, index
# projection becomes a write vector.
have_hypopg="$(psql "$DB_URL" -At -v ON_ERROR_STOP=1 -c "
SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'hypopg');
")"

if [ "$have_hypopg" != "t" ]; then
  if [ "${ALLOW_MISSING_HYPOPG:-0}" = "1" ]; then
    echo "WARN: hypopg is not installed; skipping the index-projection boundary check." >&2
  else
    echo "ERROR: hypopg is not installed, so index cost projection would silently" >&2
    echo "       degrade to the labeled heuristic and this boundary goes unverified." >&2
    echo "       Build Postgres from tools/docker/postgres-hypopg.Dockerfile, or set" >&2
    echo "       ALLOW_MISSING_HYPOPG=1 to skip this check deliberately." >&2
    exit 1
  fi
else
  hypopg_schema="$(psql "$DB_URL" -At -v ON_ERROR_STOP=1 -c "
  SELECT n.nspname FROM pg_extension e JOIN pg_namespace n ON n.oid = e.extnamespace
  WHERE e.extname = 'hypopg';
  ")"

  # ON_ERROR_STOP is deliberately off: the INSERT and CREATE TABLE are expected
  # to fail, and each is wrapped in a savepoint so the transaction survives to
  # the next assertion. Nothing is ever committed.
  hypopg_out="$(psql "$READONLY_URL" -At 2>&1 <<SQL || true
BEGIN;
SET LOCAL transaction_read_only = off;
SELECT 'HYPOPG_INDEX=' || indexname
  FROM ${hypopg_schema}.hypopg_create_index('CREATE INDEX ON demo.sales (date)');
SAVEPOINT pgqn_write_probe;
INSERT INTO demo.sales (id, date, product_category, product_name, quantity, unit_price, total_amount, region, sales_rep)
VALUES (gen_random_uuid(), DATE '2000-01-01', 'probe', 'probe', 1, 1, 1, 'probe', 'probe');
ROLLBACK TO SAVEPOINT pgqn_write_probe;
SAVEPOINT pgqn_ddl_probe;
CREATE TABLE demo.pgqn_hypopg_forbidden (id int);
ROLLBACK TO SAVEPOINT pgqn_ddl_probe;
ROLLBACK;
SQL
)"

  if ! printf '%s' "$hypopg_out" | grep -q 'HYPOPG_INDEX='; then
    echo "ERROR: readonly role could not create a hypothetical index; index cost" >&2
    echo "       projection would fall back to the heuristic. Output:" >&2
    printf '%s\n' "$hypopg_out" >&2
    exit 1
  fi

  # Two failures expected, both on privilege — not on the read-only flag, which
  # this transaction has already turned off.
  denials="$(printf '%s' "$hypopg_out" | grep -c 'permission denied' || true)"
  if [ "$denials" -lt 2 ]; then
    echo "ERROR: with transaction_read_only lifted for hypopg, the readonly role was" >&2
    echo "       able to write or run DDL. Expected 2 permission denials, saw ${denials}:" >&2
    printf '%s\n' "$hypopg_out" >&2
    exit 1
  fi
  if printf '%s' "$hypopg_out" | grep -qi 'read-only transaction'; then
    echo "ERROR: writes were blocked only by the read-only transaction flag, which" >&2
    echo "       hypopg requires lifting. The role needs privilege-level protection:" >&2
    printf '%s\n' "$hypopg_out" >&2
    exit 1
  fi

  # Belt and braces: nothing above may have leaked out of the rolled-back tx.
  leaked="$(psql "$DB_URL" -At -v ON_ERROR_STOP=1 -c "
  SELECT to_regclass('demo.pgqn_hypopg_forbidden') IS NOT NULL;
  ")"
  if [ "$leaked" = "t" ]; then
    echo "ERROR: the DDL probe table survived; the transaction was not rolled back" >&2
    exit 1
  fi

  echo "OK: hypopg index projection works for the analytical role, and writes/DDL"
  echo "    stay denied on privilege even with transaction_read_only lifted."
fi

echo "OK: readonly role cannot write, create, bypass RLS, read app metadata, or read blocked public objects."
