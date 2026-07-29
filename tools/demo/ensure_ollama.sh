#!/usr/bin/env bash
# Start in-compose Ollama and ensure the configured model is pulled for Ask / NL workbench.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

MODEL="${LLM_MODEL:-llama3.2}"
MODEL_BASE="${MODEL%%:*}"
COMPOSE=(docker compose)

ollama_has_model() {
  "${COMPOSE[@]}" exec -T ollama ollama list 2>/dev/null \
    | awk 'NR>1 { split($1, parts, ":"); print parts[1] }' \
    | grep -qxF "$MODEL_BASE"
}

echo "==> Ollama (local LLM for Ask / natural language)"
echo "    model=$MODEL"

if ! command -v docker >/dev/null 2>&1; then
  echo "ERROR: docker is required" >&2
  exit 1
fi

echo "==> Starting Ollama container"
"${COMPOSE[@]}" up -d ollama

echo "==> Waiting for Ollama API"
for i in $(seq 1 90); do
  if "${COMPOSE[@]}" exec -T ollama ollama list >/dev/null 2>&1; then
    echo "    Ollama ready after ${i}s"
    break
  fi
  if [[ "$i" -eq 90 ]]; then
    echo "ERROR: Ollama did not become ready" >&2
    "${COMPOSE[@]}" logs --tail=60 ollama || true
    exit 1
  fi
  sleep 2
done

if ollama_has_model; then
  echo "    Model $MODEL_BASE already present"
else
  echo "==> Pulling $MODEL (first run can take several minutes)..."
  if ! "${COMPOSE[@]}" exec -T ollama ollama pull "$MODEL"; then
    echo "ERROR: failed to pull $MODEL" >&2
    exit 1
  fi
  if ! ollama_has_model; then
    echo "ERROR: $MODEL not listed after pull" >&2
    "${COMPOSE[@]}" exec -T ollama ollama list || true
    exit 1
  fi
  echo "    Model $MODEL_BASE ready"
fi

echo "==> Ollama ready — workbench Ask / Generate report can use natural language"
