#!/usr/bin/env bash
# Bootstrap a local solo demo stack: Postgres + app + optional 10M seed + NYC + Ollama check.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

SEED_LARGE="${SEED_LARGE:-1}"
WITH_NYC="${WITH_NYC:-0}"
BASE_URL="${BASE_URL:-http://127.0.0.1:8080}"
COMPOSE=(docker compose)

echo "==> PgQueryNarrative demo bootstrap"
echo "    ROOT=$ROOT"
echo "    SEED_LARGE=$SEED_LARGE  WITH_NYC=$WITH_NYC"

if ! command -v docker >/dev/null 2>&1; then
  echo "ERROR: docker is required" >&2
  exit 1
fi

if [[ "$WITH_NYC" == "1" ]]; then
  export DATABASE_ALLOWED_SCHEMAS="${DATABASE_ALLOWED_SCHEMAS:-demo,opendata}"
  echo "==> DATABASE_ALLOWED_SCHEMAS=$DATABASE_ALLOWED_SCHEMAS"
fi

echo "==> Starting Postgres + app (make start-docker)"
# start-docker builds a fresh app image (avoids stale-binary "pool unavailable" failures).
make start-docker

echo "==> Ensuring Ollama + model for Ask / natural language workbench"
chmod +x ./tools/demo/ensure_ollama.sh
./tools/demo/ensure_ollama.sh

echo "==> Waiting for /ready"
for i in $(seq 1 90); do
  if curl -fsS "$BASE_URL/ready" >/dev/null 2>&1; then
    echo "    ready after ${i}s"
    break
  fi
  if [[ "$i" -eq 90 ]]; then
    echo "ERROR: app not ready at $BASE_URL/ready" >&2
    "${COMPOSE[@]}" logs --tail=80 app || true
    exit 1
  fi
  sleep 1
done

# Migrations already applied in start-docker via migrate-docker; re-run is idempotent.
echo "==> Confirming migrations (make migrate-docker)"
make migrate-docker || true

if [[ "$SEED_LARGE" == "1" ]]; then
  echo "==> Seeding large demo.sales (make seed-large-docker) — this can take several minutes"
  make seed-large-docker
else
  echo "==> Skipping seed-large (SEED_LARGE=0); using small seed from start-docker"
fi

if [[ "$WITH_NYC" == "1" ]]; then
  echo "==> Seeding NYC TLC (make seed-nyc-docker)"
  make seed-nyc-docker
  echo "==> Recreating app so DATABASE_ALLOWED_SCHEMAS includes opendata"
  DATABASE_ALLOWED_SCHEMAS="${DATABASE_ALLOWED_SCHEMAS}" "${COMPOSE[@]}" up -d --force-recreate app
  for i in $(seq 1 60); do
    if curl -fsS "$BASE_URL/ready" >/dev/null 2>&1; then
      echo "    app ready after recreate"
      break
    fi
    sleep 1
  done
fi

echo "==> Ollama check (Ask / natural language in workbench)"
OLLAMA_MODEL_BASE="${LLM_MODEL:-llama3.2}"
OLLAMA_MODEL_BASE="${OLLAMA_MODEL_BASE%%:*}"
if docker compose exec -T ollama ollama list 2>/dev/null | awk 'NR>1 { split($1, parts, ":"); print parts[1] }' | grep -qxF "$OLLAMA_MODEL_BASE"; then
  echo "    In-compose Ollama ready (model $OLLAMA_MODEL_BASE)"
elif curl -fsS http://127.0.0.1:11434/api/tags >/dev/null 2>&1; then
  echo "    Host Ollama reachable at :11434 (set LLM_BASE_URL=http://host.docker.internal:11434 if needed)"
else
  echo "WARN: Ollama not ready — run ./tools/demo/ensure_ollama.sh"
fi

echo
echo "==> Bootstrap complete"
echo "    UI:     $BASE_URL"
echo "    Smoke:  ./tools/demo/smoke_scenes.sh"
echo "    Orgs:   ./tools/demo/multi_org_demo.sh"
echo "    Open http://localhost:8080 → Investigate"
