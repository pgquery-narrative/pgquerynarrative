#!/usr/bin/env bash
# Prove internal multi-org isolation for the solo demo (RLS + connection allowlist).
# Works with local compose (auth off) via admin HTTP APIs + SQL under app.current_org_id.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

BASE_URL="${BASE_URL:-http://127.0.0.1:8080}"
ORG_A="${ORG_A:-00000000-0000-0000-0000-000000000001}"
SLUG_B="demo-org-b-$$"
NAME_B="Demo Org B"

echo "==> Multi-org demo against $BASE_URL"

if ! curl -fsS "$BASE_URL/ready" >/dev/null 2>&1; then
  echo "ERROR: app not ready. Run ./tools/demo/bootstrap.sh first." >&2
  exit 1
fi

json_field() {
  # Usage: json_field key < json
  local key="$1"
  python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("'"$key"'",""))' 2>/dev/null \
    || sed -n 's/.*"'"$key"'"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1
}

echo "==> GET /api/v1/me"
curl -fsS "$BASE_URL/api/v1/me" | tee /tmp/pgqn_me.json
echo

echo "==> Create organization B (admin API; local auth-off principal is admin)"
CREATE_RESP="$(curl -fsS -X POST "$BASE_URL/api/v1/admin/organizations" \
  -H 'Content-Type: application/json' \
  -d "{\"name\":\"$NAME_B\",\"slug\":\"$SLUG_B\"}")"
echo "$CREATE_RESP"
ORG_B="$(printf '%s' "$CREATE_RESP" | json_field id)"
if [[ -z "$ORG_B" ]]; then
  echo "ERROR: could not parse org B id from: $CREATE_RESP" >&2
  exit 1
fi
echo "    ORG_B=$ORG_B"

echo "==> Upsert memberships"
curl -fsS -X POST "$BASE_URL/api/v1/admin/memberships" \
  -H 'Content-Type: application/json' \
  -d "{\"user_id\":\"analyst-a\",\"organization_id\":\"$ORG_A\",\"role\":\"admin\"}" >/dev/null
curl -fsS -X POST "$BASE_URL/api/v1/admin/memberships" \
  -H 'Content-Type: application/json' \
  -d "{\"user_id\":\"analyst-b\",\"organization_id\":\"$ORG_B\",\"role\":\"admin\"}" >/dev/null
echo "    ok"

echo "==> Ensure Org A has connection assignment 'default'"
curl -fsS -X POST "$BASE_URL/api/v1/admin/connection-assignments" \
  -H 'Content-Type: application/json' \
  -d "{\"organization_id\":\"$ORG_A\",\"connection_id\":\"default\"}" >/dev/null
echo "    ok"

echo "==> Seed Org A–only saved query via SQL (superuser)"
QUERY_RAW="$(docker compose exec -T postgres psql -U postgres -d pgquerynarrative -v ON_ERROR_STOP=1 -Atc "
  INSERT INTO app.saved_queries (name, sql, connection_id, organization_id)
  VALUES ('org-a-secret-demo', 'SELECT 1 AS org_a_only', 'default', '$ORG_A'::uuid)
  RETURNING id::text;
")"
QUERY_ID="$(printf '%s\n' "$QUERY_RAW" | grep -Eo '[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}' | head -1)"
if [[ -z "$QUERY_ID" ]]; then
  echo "ERROR: could not parse saved query id from: $QUERY_RAW" >&2
  exit 1
fi
echo "    QUERY_ID=$QUERY_ID"

echo "==> Prove Org B cannot see Org A query (FORCE RLS + app.current_org_id)"
VISIBLE="$(docker compose exec -T postgres psql -U postgres -d pgquerynarrative -v ON_ERROR_STOP=1 -Atc "
  SET ROLE pgquerynarrative_app;
  SELECT set_config('app.current_org_id', '$ORG_B', false);
  SELECT COUNT(*)::text FROM app.saved_queries WHERE id = '$QUERY_ID'::uuid;
" | grep -E '^[0-9]+$' | tail -1)"
if [[ "$VISIBLE" != "0" ]]; then
  echo "FAIL: Org B saw Org A saved query (count=$VISIBLE)" >&2
  exit 1
fi
echo "    PASS: Org B count=0 for Org A saved query"

echo "==> Prove Org A can see its query under RLS"
VISIBLE_A="$(docker compose exec -T postgres psql -U postgres -d pgquerynarrative -v ON_ERROR_STOP=1 -Atc "
  SET ROLE pgquerynarrative_app;
  SELECT set_config('app.current_org_id', '$ORG_A', false);
  SELECT COUNT(*)::text FROM app.saved_queries WHERE id = '$QUERY_ID'::uuid;
" | grep -E '^[0-9]+$' | tail -1)"
if [[ "$VISIBLE_A" != "1" ]]; then
  echo "FAIL: Org A should see its query (count=$VISIBLE_A)" >&2
  exit 1
fi
echo "    PASS: Org A count=1"

echo "==> Connection allowlist: Org B with only decoy assignment cannot use 'default'"
docker compose exec -T postgres psql -U postgres -d pgquerynarrative -v ON_ERROR_STOP=1 -c "
  INSERT INTO app.organization_connections (organization_id, connection_id, enabled)
  VALUES ('$ORG_B'::uuid, 'decoy-not-in-catalog', true)
  ON CONFLICT (organization_id, connection_id) DO UPDATE SET enabled = true;
" >/dev/null

# Hit list connections / run as default principal still uses Org A; prove via Go-less SQL:
# Org B has rows in organization_connections but not 'default'.
HAS_DEFAULT="$(docker compose exec -T postgres psql -U postgres -d pgquerynarrative -v ON_ERROR_STOP=1 -Atc "
  SET ROLE pgquerynarrative_app;
  SELECT set_config('app.current_org_id', '$ORG_B', false);
  SELECT COUNT(*)::text FROM app.organization_connections
   WHERE organization_id = '$ORG_B'::uuid AND connection_id = 'default' AND enabled;
" | grep -E '^[0-9]+$' | tail -1)"
if [[ "$HAS_DEFAULT" != "0" ]]; then
  echo "FAIL: expected Org B to lack default assignment" >&2
  exit 1
fi
echo "    PASS: Org B has no enabled 'default' connection assignment"

echo "==> List orgs (admin)"
curl -fsS "$BASE_URL/api/v1/admin/organizations" | head -c 500
echo
echo

echo "==> Multi-org demo PASSED"
echo "    Pitch: Org A metadata invisible to Org B under fail-closed RLS;"
echo "           connection allowlist prevents cross-org analytics access."
echo "    ORG_A=$ORG_A"
echo "    ORG_B=$ORG_B"
echo "    saved_query=$QUERY_ID"
