#!/usr/bin/env sh
# Verify the PostgreSQL extension in infra/postgres-extension against a throwaway PostgreSQL.
#
# Starts its own container (never touches the docker-compose stack), then checks that:
#   - a fresh 1.1 install and a 1.0 -> 1.1 upgrade end in the same state
#   - nothing is executable by PUBLIC, and access needs an explicit grant
#   - a role cannot redirect the API URL, and cannot change it at all
#   - with the http extension, the API URL and the caller's own API key are what is sent
#
# Requires: docker. The http checks also need network access to apt (they are skipped with a
# warning if the pgsql-http package cannot be installed).
# Usage: sh tools/db/verify-extension.sh        (PG_IMAGE=postgres:16 by default)
set -eu

ROOT_DIR="$(cd "$(dirname "$0")/../.." && pwd)"
EXT_DIR="$ROOT_DIR/infra/postgres-extension"
PG_IMAGE="${PG_IMAGE:-postgres:16}"
C="pgqn-ext-verify-$$"
PASS=0
FAIL=0

cleanup() { docker rm -f "$C" >/dev/null 2>&1 || true; }
trap cleanup EXIT INT TERM

ok()  { PASS=$((PASS + 1)); echo "  PASS  $1"; }
bad() { FAIL=$((FAIL + 1)); echo "  FAIL  $1"; }

# psql as a given role against a given database; extra args pass through to psql.
run() { role="$1"; db="$2"; shift 2; docker exec -i "$C" psql -X -q -At -v ON_ERROR_STOP=1 -U "$role" -d "$db" "$@"; }
su()  { run postgres "$@"; }

# expect_ok LABEL ROLE DB SQL
expect_ok() {
  if out="$(run "$2" "$3" -c "$4" 2>&1)"; then ok "$1"; else bad "$1 -> $out"; fi
}
# expect_denied LABEL ROLE DB SQL: must fail with 'permission denied'
expect_denied() {
  if out="$(run "$2" "$3" -c "$4" 2>&1)"; then
    bad "$1 -> succeeded, expected permission denied"
  elif echo "$out" | grep -q 'permission denied'; then ok "$1"; else bad "$1 -> $out"; fi
}
# expect_eq LABEL WANT GOT
expect_eq() { if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 -> got '$3', wanted '$2'"; fi; }

echo "== Starting throwaway PostgreSQL ($PG_IMAGE)"
docker run -d --name "$C" -e POSTGRES_PASSWORD=x "$PG_IMAGE" >/dev/null
i=0
until docker exec "$C" pg_isready -U postgres >/dev/null 2>&1 && su postgres -c 'SELECT 1' >/dev/null 2>&1; do
  i=$((i + 1)); [ "$i" -gt 60 ] && { echo "PostgreSQL did not start" >&2; exit 1; }
  sleep 1
done

SHARE="$(docker exec "$C" pg_config --sharedir)/extension"
docker cp "$EXT_DIR/pgquerynarrative.control" "$C:$SHARE/"
for f in "$EXT_DIR"/pgquerynarrative--*.sql; do docker cp "$f" "$C:$SHARE/"; done

su postgres -c "CREATE DATABASE fresh"
su postgres -c "CREATE DATABASE upgraded"
su postgres -c "CREATE ROLE alice LOGIN"
su postgres -c "CREATE ROLE bob LOGIN"

echo "== Fresh install of 1.1 and upgrade from 1.0 end in the same state"
su fresh -c "CREATE EXTENSION pgquerynarrative"
su upgraded -c "CREATE EXTENSION pgquerynarrative VERSION '1.0'"
su upgraded -c "ALTER EXTENSION pgquerynarrative UPDATE"
FP="SELECT string_agg(x, E'\n' ORDER BY x) FROM (
  SELECT 'fn ' || p.oid::regprocedure::text || ' definer=' || p.prosecdef || ' acl=' || COALESCE(p.proacl::text, 'default')
         || ' md5=' || md5(pg_get_functiondef(p.oid)) AS x
  FROM pg_depend d JOIN pg_proc p ON p.oid = d.objid AND d.classid = 'pg_proc'::regclass
  WHERE d.refobjid = (SELECT oid FROM pg_extension WHERE extname = 'pgquerynarrative') AND d.deptype = 'e'
  UNION ALL
  SELECT 'ver ' || extversion || ' config_tables=' || COALESCE(array_length(extconfig, 1), 0) FROM pg_extension WHERE extname = 'pgquerynarrative'
  UNION ALL
  SELECT 'tbl ' || c.relname || ' acl=' || COALESCE(c.relacl::text, 'default')
  FROM pg_class c WHERE c.relname = 'pgquerynarrative_config'
) t"
F1="$(su fresh -c "$FP")"; F2="$(su upgraded -c "$FP")"
expect_eq "fresh 1.1 equals upgraded 1.0 -> 1.1" "$F1" "$F2"
expect_eq "extension is at 1.1" "1.1" "$(su fresh -c "SELECT extversion FROM pg_extension WHERE extname = 'pgquerynarrative'")"

echo "== PUBLIC holds nothing"
n="$(su fresh -c "SELECT count(*) FROM pg_depend d JOIN pg_proc p ON p.oid = d.objid AND d.classid = 'pg_proc'::regclass,
  LATERAL aclexplode(COALESCE(p.proacl, acldefault('f', p.proowner))) a
  WHERE d.refobjid = (SELECT oid FROM pg_extension WHERE extname = 'pgquerynarrative') AND d.deptype = 'e'
    AND a.grantee = 0")"
expect_eq "no extension function is executable by PUBLIC" "0" "$n"
expect_denied "unprivileged role cannot call run_query" alice fresh "SELECT pgquerynarrative_run_query('SELECT 1', 1)"
expect_denied "unprivileged role cannot call generate_report" alice fresh "SELECT pgquerynarrative_generate_report('SELECT 1')"
expect_denied "unprivileged role cannot call list_saved" alice fresh "SELECT pgquerynarrative_list_saved()"
expect_denied "unprivileged role cannot read the URL" alice fresh "SELECT pgquerynarrative_get_api_url()"
expect_denied "unprivileged role cannot set the URL" alice fresh "SELECT pgquerynarrative_set_api_url('http://evil.example')"
expect_denied "unprivileged role cannot grant itself access" alice fresh "SELECT pgquerynarrative_grant_access('alice')"
expect_denied "unprivileged role cannot read the config table" alice fresh "SELECT * FROM pgquerynarrative_config"

echo "== Granting access"
su fresh -c "SELECT pgquerynarrative_grant_access('alice')"
expect_ok "granted role can call run_query" alice fresh "SELECT pgquerynarrative_run_query('SELECT 1', 1)"
expect_ok "granted role can read the URL" alice fresh "SELECT pgquerynarrative_get_api_url()"
expect_denied "granted role still cannot set the URL" alice fresh "SELECT pgquerynarrative_set_api_url('http://evil.example')"
expect_denied "grant did not reach another role" bob fresh "SELECT pgquerynarrative_run_query('SELECT 1', 1)"
su fresh -c "SELECT pgquerynarrative_revoke_access('alice')"
expect_denied "revoked role is denied again" alice fresh "SELECT pgquerynarrative_run_query('SELECT 1', 1)"
su fresh -c "SELECT pgquerynarrative_grant_access('alice')"

echo "== The API URL cannot be redirected by a role"
expect_eq "default URL" "http://localhost:8080" "$(su fresh -c "SELECT pgquerynarrative_get_api_url()")"
run alice fresh -c "SET pgquerynarrative.api_url = 'http://evil.example'; SELECT pgquerynarrative_get_api_url()" >/tmp/pgqn-ext-$$.out 2>&1 || true
expect_eq "SET pgquerynarrative.api_url has no effect" "http://localhost:8080" "$(tail -n 1 /tmp/pgqn-ext-$$.out)"
rm -f /tmp/pgqn-ext-$$.out
for bad_url in 'file:///etc/passwd' 'ftp://x' 'http://a b' ''; do
  if su fresh -c "SELECT pgquerynarrative_set_api_url('$bad_url')" >/dev/null 2>&1; then bad "owner set invalid URL '$bad_url'"; else ok "owner cannot set invalid URL '$bad_url'"; fi
done
su fresh -c "SELECT pgquerynarrative_set_api_url('https://pqn.example.com:8443/')"
expect_eq "owner can set the URL, trailing slash trimmed" "https://pqn.example.com:8443" "$(run alice fresh -c "SELECT pgquerynarrative_get_api_url()")"

echo "== With the http extension: URL and the caller's own key are what is sent"
if docker exec "$C" sh -c "apt-get update -qq >/dev/null 2>&1 && apt-get install -y -qq postgresql-16-http python3 >/dev/null 2>&1"; then
  su postgres -c "CREATE DATABASE withhttp"
  su withhttp -c "CREATE EXTENSION http"
  su withhttp -c "CREATE EXTENSION pgquerynarrative"
  su withhttp -c "SELECT pgquerynarrative_grant_access('alice')"
  su withhttp -c "SELECT pgquerynarrative_set_api_url('http://127.0.0.1:18099/')"
  docker exec -d "$C" python3 -c "
import http.server, json
class H(http.server.BaseHTTPRequestHandler):
    def _r(self):
        n = int(self.headers.get('Content-Length') or 0)
        body = self.rfile.read(n).decode() if n else ''
        out = json.dumps({'path': self.path, 'method': self.command, 'auth': self.headers.get('Authorization'), 'ctype': self.headers.get('Content-Type'), 'body': body}).encode()
        self.send_response(200); self.send_header('Content-Type', 'application/json'); self.send_header('Content-Length', str(len(out))); self.end_headers(); self.wfile.write(out)
    do_GET = do_POST = _r
    def log_message(self, *a): pass
http.server.HTTPServer(('127.0.0.1', 18099), H).serve_forever()
"
  sleep 2
  R="$(run alice withhttp -c "SELECT pgquerynarrative_run_query('SELECT 1', 5)")"
  expect_eq "run_query without a key sends no Authorization" "null" "$(echo "$R" | docker exec -i "$C" python3 -c 'import sys,json; print(json.dumps(json.load(sys.stdin)["auth"]))')"
  expect_eq "run_query goes to the configured URL and path" "/api/v1/queries/run POST" "$(echo "$R" | docker exec -i "$C" python3 -c 'import sys,json; d=json.load(sys.stdin); print(d["path"], d["method"])')"
  expect_eq "run_query sends a JSON body with the right content type" 'application/json {"sql" : "SELECT 1", "limit" : 5}' "$(echo "$R" | docker exec -i "$C" python3 -c 'import sys,json; d=json.load(sys.stdin); print(d["ctype"], d["body"])')"
  R="$(run alice withhttp -c "SELECT pgquerynarrative_set_api_key('k-alice'); SELECT pgquerynarrative_run_query('SELECT 1', 5)" | tail -n 1)"
  expect_eq "run_query sends the caller's key as a Bearer token" '"Bearer k-alice"' "$(echo "$R" | docker exec -i "$C" python3 -c 'import sys,json; print(json.dumps(json.load(sys.stdin)["auth"]))')"
  R="$(run alice withhttp -c "SELECT pgquerynarrative_set_api_key('k-alice'); SELECT pgquerynarrative_list_saved(10, 0)" | tail -n 1)"
  expect_eq "list_saved (GET) works and sends the key" 'GET /api/v1/queries/saved?limit=10&offset=0 "Bearer k-alice"' "$(echo "$R" | docker exec -i "$C" python3 -c 'import sys,json; d=json.load(sys.stdin); print(d["method"], d["path"], json.dumps(d["auth"]))')"
  R="$(run alice withhttp -c "SELECT pgquerynarrative_run_query('SELECT 1', 5)")"
  expect_eq "another session does not inherit the key" "null" "$(echo "$R" | docker exec -i "$C" python3 -c 'import sys,json; print(json.dumps(json.load(sys.stdin)["auth"]))')"
else
  echo "  SKIP  http checks: could not install postgresql-16-http (no network, or PG_IMAGE is not Debian based)"
fi

echo
echo "RESULT extension: pass=$PASS fail=$FAIL"
[ "$FAIL" -eq 0 ]
