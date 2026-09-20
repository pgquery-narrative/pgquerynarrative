#!/usr/bin/env bash
# Run the commands in the pqn documentation exactly as written, and check what they print.
#   docs/getting-started/pqn-extension.md    every ```bash block, in order (the quick start, on the image)
#   docs/getting-started/pqn-installation.md every ```bash block, then the upgrade, backup and
#                                            restore, and uninstall steps against real servers
# So neither page can drift from the code. It starts containers named pqn-quickstart (port 5433),
# pqn-install and pqn-target, and removes them again.
#
# Requires: docker, go and a C toolchain (make build-pqn), python3.
# Usage: bash tools/db/verify-pqn-docs.sh
#        PQN_DOCS_OUTPUT_FILE=/tmp/out.txt bash tools/db/verify-pqn-docs.sh   (also keep the real output)
set -eu

ROOT_DIR="$(cd "$(dirname "$0")/../.." && pwd)"
PAGE="$ROOT_DIR/docs/getting-started/pqn-extension.md"
OUT="$(mktemp /tmp/pqn-docs-run.XXXXXX)"
SCRIPT="$(mktemp /tmp/pqn-docs-blocks.XXXXXX)"
DUMP="$(mktemp /tmp/pqn-docs-dump.XXXXXX)"
INSTALL_PAGE="$ROOT_DIR/docs/getting-started/pqn-installation.md"
PASS=0
FAIL=0
cleanup() { docker rm -f pqn-quickstart pqn-install pqn-target pqn-dba >/dev/null 2>&1 || true; rm -f "$OUT" "$SCRIPT" "$DUMP"; }
trap cleanup EXIT INT TERM

ok()  { PASS=$((PASS + 1)); echo "  PASS  $1"; }
bad() { FAIL=$((FAIL + 1)); echo "  FAIL  $1"; }

python3 - "$PAGE" "$SCRIPT" <<'PY'
import re, sys
page, out = sys.argv[1], sys.argv[2]
blocks = re.findall(r"^```bash\n(.*?)^```$", open(page).read(), re.S | re.M)
open(out, "w").write("set -euo pipefail\n" + "\n".join(blocks))
print(len(blocks), "bash blocks extracted from", page)
PY

echo "== Running the quick start exactly as written"
cd "$ROOT_DIR"
if bash "$SCRIPT" >"$OUT" 2>&1; then ok "every command on the page exited as the page says it should"
else bad "a command on the page failed. Last output:"; tail -25 "$OUT"; fi

check() { if grep -qE -- "$2" "$OUT"; then ok "$1"; else bad "$1 (no match for '$2')"; fi; }
echo "== What the page says it prints"
check "the extension is at 1.1"                   "^pqn 1\.1$"
check "expose_sql prints the view"                "CREATE VIEW pqn.orders AS SELECT id, customer_id, created_at, status, amount FROM shop.orders"
check "the tool reports its version"              "^pqn dev$"
check "top ranks the slow statement"              "date_trunc\(\\\$1, created_at\) = \\\$2"
check "investigate shows the plan findings"       "\[high\] seq_scan on orders"
check "investigate proposes a rewrite"            "1\. function_wrap \(confidence high\)"
check "investigate verifies it"                   "same rows, [0-9.]+x faster \(recorded by the database\)"
check "a wrong rewrite is Different"              "^Verdict: Different$"
check "the ledger lists both investigations"      "daily orders are slow"
check "run works over the curated view"           "^\(4 row\(s\)\)$"
check "DELETE is refused by the tool"             "the statement is not accepted: only SELECT statements are allowed"
check "a file read is refused by the tool"        "query calls a function that is not allowed"
check "a hidden column is refused by the database" "permission denied for schema shop"
check "doctor finds nothing blocking"             "^0 blocking, [0-9]+ warning\(s\)\.$"


# ---------------------------------------------------------------------------------------------
# The installation guide
# ---------------------------------------------------------------------------------------------
# section PAGE "Heading" LANG: the fenced blocks of one language under one ## heading.
section() {
python3 - "$1" "$2" "$3" <<'PY'
import re, sys
page, heading, lang = sys.argv[1:4]
text = open(page).read()
parts = re.split(r"^## ", text, flags=re.M)
for part in parts:
    if part.split("\n", 1)[0].strip() == heading:
        print("\n".join(re.findall(r"^```%s\n(.*?)^```$" % lang, part, re.S | re.M)))
        break
else:
    sys.exit("no section called %r in %s" % (heading, page))
PY
}
allblocks() { python3 - "$1" "$2" <<'PY'
import re, sys
print("\n".join(re.findall(r"^```%s\n(.*?)^```$" % sys.argv[2], open(sys.argv[1]).read(), re.S | re.M)))
PY
}

wait_pg() { i=0; until docker exec "$1" psql -U postgres -Atc 'SELECT 1' >/dev/null 2>&1; do i=$((i + 1)); [ "$i" -gt 60 ] && { echo "PostgreSQL did not start" >&2; return 1; }; sleep 1; done; sleep 2; }
psql() { docker exec -i pqn-install psql -X -q "$@"; }
cpfiles() { SHARE2="$(docker exec "$1" pg_config --sharedir)/extension"; for f in pqn.control pqn--1.0.sql pqn--1.0--1.1.sql; do docker cp "infra/pqn-extension/$f" "$1:$SHARE2/"; done; }
PQN_ROLES="'pqn_owner','pqn_reader','pqn_stats','pqn_ledger','pqn_viewer','pqn_analyst','pqn_admin'"

echo
echo "== The installation guide: its bash blocks, as written"
docker rm -f pqn-install pqn-target >/dev/null 2>&1 || true
docker run -d --name pqn-install -e POSTGRES_PASSWORD=postgres postgres:18 -c shared_preload_libraries=pg_stat_statements >/dev/null
wait_pg pqn-install
cpfiles pqn-install                      # what step 1 does on a real server
docker exec pqn-install psql -X -q -U postgres -c "CREATE DATABASE app" -c "CREATE DATABASE app_up" -c "CREATE DATABASE reports"

allblocks "$INSTALL_PAGE" bash > "$SCRIPT.install"
INSTALL_OUT="$(mktemp /tmp/pqn-docs-install.XXXXXX)"
if ( set -euo pipefail; psql() { docker exec -i pqn-install psql -X -q "$@"; }; . "$SCRIPT.install" ) >"$INSTALL_OUT" 2>&1; then ok "every bash block of the installation guide ran and exited as written"
else bad "a bash block of the installation guide failed. Last output:"; tail -20 "$INSTALL_OUT"; fi
rm -f "$SCRIPT.install"

icheck() { if grep -qE -- "$2" "$INSTALL_OUT"; then ok "$1"; else bad "$1 (no match for '$2')"; fi; }
icheck "PostgreSQL sees the extension files at version 1.1"   "^pqn 1\.1$"
icheck "init creates the ledger and the registry"            "^ledger at version 2, exposure registry ready$"
icheck "expose_sql prints the view"                          "CREATE VIEW pqn.people AS SELECT id, dept FROM hr.people;"
icheck "enroll_sql prints the group grant"                   "^GRANT pqn_analyst TO alice;$"
icheck "enroll_sql prints the statement timeout"             "^ALTER ROLE alice SET statement_timeout = '15s';$"
icheck "enroll_sql prints the lock timeout"                  "^ALTER ROLE alice SET lock_timeout = '2s';$"
icheck "enroll_sql prints the idle timeout"                  "^ALTER ROLE alice SET idle_in_transaction_session_timeout = '10s';$"
icheck "enroll_sql records the limit where the person cannot reach it" "^SELECT pqn_api.record_limit\('alice', 15000\);$"
icheck "enroll_sql prints temp_file_limit for a superuser"   "^ALTER ROLE alice SET temp_file_limit = '1GB';$"
icheck "enforce_limits runs and has nothing to cancel"       "^limits enforced: 0$"
if grep -qE "^BLOCK " "$INSTALL_OUT"; then bad "the setup check found a BLOCK on the page's own setup"; else ok "the setup check finds nothing blocking"; fi
icheck "the guide's expected warning: pg_stat_statements is readable by PUBLIC" "^WARN reader sees pg_stat_statements$"
icheck "the guide's expected warning: TEMP"                  "^WARN reader TEMP privilege$"
if grep -q "login can become an owner role" "$INSTALL_OUT"; then bad "a superuser install, or a revoked installer, still warns about owner roles"; else ok "no warning about owner roles after the steps"; fi
rm -f "$INSTALL_OUT"
[ "$(psql -U postgres -d reports -At -c "SELECT extversion FROM pg_extension WHERE extname = 'pqn'")" = "1.1" ] && ok "installing without a superuser produced the extension too" || bad "the installer-role path did not create the extension"
[ "$(psql -U postgres -d reports -At -c "SELECT count(*) FROM pg_auth_members m JOIN pg_roles u ON u.oid = m.member JOIN pg_roles r ON r.oid = m.roleid WHERE u.rolname = 'pqn_installer' AND r.rolname IN ('pqn_owner','pqn_reader','pqn_stats','pqn_ledger')")" = "0" ] && ok "the installer holds no owner role afterwards" || bad "the installer still holds an owner role"

echo "== The upgrade steps, on a database that has 1.0"
psql -U postgres -d app_up -c "CREATE EXTENSION pqn VERSION '1.0'"
psql -U postgres -d app_up -At -c "SELECT pqn_api.init()" >/dev/null
[ "$(psql -U postgres -d app_up -At -c "SELECT extversion FROM pg_extension WHERE extname = 'pqn'")" = "1.0" ] && ok "the database starts at 1.0" || bad "the database did not start at 1.0"
section "$INSTALL_PAGE" "Upgrade" shell | grep -vE '^(make |install -m)' | sed 's/-d app /-d app_up /g' > "$SCRIPT.up"
[ -s "$SCRIPT.up" ] || bad "the guide has no Upgrade block"
if ( set -euo pipefail; psql() { docker exec -i pqn-install psql -X -q "$@"; }; . "$SCRIPT.up" ) >/dev/null 2>&1; then ok "the upgrade steps run as written"; else bad "the upgrade steps failed"; fi
[ "$(psql -U postgres -d app_up -At -c "SELECT extversion FROM pg_extension WHERE extname = 'pqn'")" = "1.1" ] && ok "the extension is at 1.1 after the upgrade" || bad "the extension did not reach 1.1"
rm -f "$SCRIPT.up"

echo "== Back up and restore, on a second server with only the extension files"
docker run -d --name pqn-target -e POSTGRES_PASSWORD=postgres postgres:18 >/dev/null
wait_pg pqn-target
cpfiles pqn-target
# Without the roles, a restore fails the way the guide says (checked on a dump of the first server).
docker exec pqn-install pg_dump -U postgres -d app -Fc > "$DUMP"
docker exec pqn-target psql -X -q -U postgres -c "CREATE DATABASE norole"
if docker exec -i pqn-target pg_restore -U postgres -d norole < "$DUMP" 2>&1 | grep -q 'ALTER SCHEMA pqn OWNER TO pqn_owner'; then ok "without the roles the restore fails on ALTER SCHEMA pqn OWNER TO pqn_owner, as the guide says"; else bad "a restore without the roles did not fail the way the guide says"; fi
# The guide's own steps: old-host is pqn-install, new-host is pqn-target.
section "$INSTALL_PAGE" "Back up and restore" shell \
  | sed -e 's/ -h old-host//g; s/ -h new-host//g' \
        -e 's/^pg_dumpall /docker exec -i pqn-install pg_dumpall /' \
        -e 's/| psql /| docker exec -i pqn-target psql -X -q /' \
        -e 's/^psql /docker exec -i pqn-target psql -X -q /' \
        -e 's/^pg_dump \(.*\) -f app.dump$/docker exec -i pqn-install pg_dump \1 > app.dump/' > "$SCRIPT.restore"
[ "$(wc -l < "$SCRIPT.restore" | tr -d ' ')" -ge 4 ] || bad "the guide's backup and restore block has fewer than four commands"
if ( set -euo pipefail; pg_restore() { f="${@: -1}"; docker exec -i pqn-target pg_restore "${@:1:$#-1}" < "$f"; }; . "$SCRIPT.restore" ) >/dev/null 2>&1; then ok "the backup and restore steps run as written"; else bad "the backup and restore steps failed"; fi
rm -f app.dump "$SCRIPT.restore"
[ "$(docker exec pqn-target psql -X -q -At -U postgres -d newdb -c "SELECT extversion FROM pg_extension WHERE extname = 'pqn'")" = "1.1" ] && ok "the restored database has the extension at 1.1" || bad "the restored database lacks the extension"
[ "$(docker exec pqn-target psql -X -q -At -U postgres -d newdb -c "SELECT count(*) FROM pqn_ledger.schema_migrations")" = "2" ] && ok "the restored database has the ledger" || bad "the restored database lacks the ledger"
[ "$(docker exec pqn-target psql -X -q -At -U postgres -d newdb -c "SELECT count(*) FROM pg_roles WHERE rolname = 'alice'")" = "1" ] && ok "the restore brought the people too" || bad "the people were not restored"
[ "$(docker exec pqn-target psql -X -q -At -U postgres -d newdb -c "SELECT count(*) FROM pqn_api.verify_setup() WHERE level = 'BLOCK'")" = "0" ] && ok "the restored setup has nothing blocking" || bad "the restored setup has a BLOCK"

echo "== A DBA who is not a superuser (the guide's claim)"
docker run -d --name pqn-dba -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=app postgres:18 >/dev/null
wait_pg pqn-dba
cpfiles pqn-dba
docker exec pqn-dba psql -X -q -U postgres -d app -c "CREATE ROLE dba LOGIN CREATEROLE" -c "CREATE ROLE inst LOGIN" -c "ALTER DATABASE app OWNER TO dba" -c "GRANT pg_monitor TO dba WITH ADMIN OPTION"
dq() { docker exec -i pqn-dba psql -X -q -At -U "$1" -d app "${@:2}"; }
if docker exec -i pqn-dba psql -X -q -U dba -d app -v installer=inst -f - < infra/pqn-extension/pqn-roles.sql >/dev/null 2>&1; then ok "a CREATEROLE DBA runs the role script"; else bad "the role script failed for a CREATEROLE DBA"; fi
dq inst -c "CREATE EXTENSION pqn" >/dev/null && dq inst -c "SELECT pqn_api.init()" >/dev/null && ok "an ordinary installer creates and initializes the extension" || bad "the installer could not create the extension"
dq dba -c "CREATE SCHEMA hr" -c "CREATE TABLE hr.t (a int, b int)" >/dev/null
[ -z "$(dq dba -c "SELECT pqn_api.expose('hr.t', ARRAY['a'])" 2>&1 | grep -i 'permission denied' || true)" ] && bad "the guide says a DBA needs the grants, but expose worked without them" || ok "without the grants the DBA cannot call expose (permission denied)"
section "$INSTALL_PAGE" "2. Create it" shell | grep -E '^psql -U dba' | sed 's/^psql /docker exec -i pqn-dba psql -X -q /' > "$SCRIPT.dba"
[ "$(wc -l < "$SCRIPT.dba" | tr -d ' ')" = "2" ] || bad "the guide should have exactly two GRANT lines for the DBA, found $(wc -l < "$SCRIPT.dba" | tr -d ' ')"
if ( set -euo pipefail; . "$SCRIPT.dba" ) >/dev/null 2>&1; then ok "the guide's two GRANT lines run as written"; else bad "the guide's GRANT lines failed"; fi
[ "$(dq dba -c "SELECT pqn_api.expose('hr.t', ARRAY['a']) IS NOT NULL")" = "t" ] && ok "after the grants the DBA can expose" || bad "the DBA still cannot expose"
dq dba -c "CREATE ROLE bob LOGIN" >/dev/null
[ "$(dq dba -c "SELECT pqn_api.enroll('bob', 'analyst') IS NOT NULL")" = "t" ] && ok "and enroll" || bad "the DBA cannot enroll"
[ "$(dq bob -c "SELECT (pqn_api.run('SELECT count(*) AS n FROM t')->'rows'->0->>'n')")" = "0" ] && ok "and the person can use it" || bad "the enrolled person cannot query"
[ "$(docker exec pqn-dba psql -X -q -At -U postgres -d app -c "SELECT count(*) FROM pg_roles WHERE rolsuper AND rolname IN ('dba','inst','bob')")" = "0" ] && ok "no superuser was involved after the server was created" || bad "a superuser was involved"
rm -f "$SCRIPT.dba"

echo "== Uninstall"
sect_body="$(section "$INSTALL_PAGE" "Uninstall" shell)"
first_block="$(printf '%s\n' "$sect_body" | awk '/^psql -U postgres -d app <</,/^SQL$/')"
printf '%s\n' "$sect_body" | grep -E '^psql -U postgres -c ' > "$SCRIPT.roles"
[ -n "$first_block" ] && [ "$(wc -l < "$SCRIPT.roles" | tr -d ' ')" = "2" ] || bad "the guide's uninstall block or role removal block is missing or has changed"
ALL_OK=1
for db in app app_up reports; do
  printf '%s\n' "$first_block" | sed "s/-d app /-d $db /" > "$SCRIPT.rm"
  ( set -euo pipefail; psql() { docker exec -i pqn-install psql -X -q "$@"; }; . "$SCRIPT.rm" ) >/dev/null 2>&1 || ALL_OK=0
done
[ "$ALL_OK" = 1 ] && ok "the removal SQL runs in every database" || bad "the removal SQL failed"
[ "$(psql -U postgres -d app -At -c "SELECT count(*) FROM pg_namespace WHERE nspname LIKE 'pqn%'")" = "0" ] && ok "no pqn schema is left behind" || bad "a pqn schema is left behind"
if ( set -euo pipefail; psql() { docker exec -i pqn-install psql -X -q "$@"; }; . "$SCRIPT.roles" ) >/dev/null 2>&1; then ok "the role removal runs as written"; else bad "the role removal failed"; fi
[ "$(psql -U postgres -d app -At -c "SELECT count(*) FROM pg_roles WHERE rolname IN ($PQN_ROLES)")" = "0" ] && ok "no pqn role is left" || bad "a pqn role is left"
[ "$(psql -U postgres -d app -At -c "SELECT count(*) FROM pg_extension WHERE extname = 'pqn'")" = "0" ] && ok "the extension is gone" || bad "the extension is still there"
rm -f "$SCRIPT.rm" "$SCRIPT.roles"

echo
echo "RESULT pqn docs: pass=$PASS fail=$FAIL"
if [ -n "${PQN_DOCS_OUTPUT_FILE:-}" ]; then cp "$OUT" "$PQN_DOCS_OUTPUT_FILE"; fi
[ "$FAIL" -eq 0 ]
