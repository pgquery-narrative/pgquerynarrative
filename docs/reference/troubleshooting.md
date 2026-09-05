# Troubleshooting

Common issues and fixes. For deployment and monitoring see [Deployment](deployment.md) and [Operations](operations.md).

---

## Environment and dependencies

| Issue | Solution |
|-------|----------|
| **Docker not found** | Install [Docker Desktop](https://www.docker.com/products/docker-desktop). Verify: `docker info`. |
| **make: command not found** | Install Make (e.g. `brew install make` on macOS). |
| **Port 8080 in use** | Use another port: `PGQUERYNARRATIVE_PORT=8081 make start-docker` or `make start-local`. See [Configuration – Server](../configuration.md#server). |
| **go mod tidy / make lint: permission denied** | The Makefile sets `GOMODCACHE=$(HOME)/.gomodcache` so Go uses a user-writable cache. If you run `go mod tidy` or `go build` outside Make, set `export GOMODCACHE=$HOME/.gomodcache` (or fix ownership of your Go module cache). |

---

## Database {#database}

| Issue | Solution |
|-------|----------|
| **PostgreSQL connection refused** | **Docker:** [Quick start](../getting-started/quickstart.md) – `make start-docker`. **Local:** start Postgres (e.g. `brew services start postgresql@18`), then `make start-local`. |
| **Role does not exist / permission denied** | Run `make db-init` then `make migrate`, or `make start-local` once (see [Installation](../getting-started/installation.md)). If `demo.sales` denied: grant SELECT to `pgquerynarrative_readonly` on the table. |
| **Unexpected results from wrong data source** | Set `connection_id` explicitly in API/MCP calls. If omitted or unknown, server falls back to `DATABASE_DEFAULT_CONNECTION_ID` (see [Configuration – Multiple database connections](../configuration.md#multiple-database-connections)). |

---

## Reports and LLM {#reports-and-llm}

| Issue | Solution |
|-------|----------|
| **Failed to parse narrative JSON** | LLM output may be truncated. Ensure Ollama is running and model pulled (`ollama serve`, `ollama pull llama3.2`). Try a larger model or restart Ollama. |
| **Report generation fails or times out** | See [LLM setup](../getting-started/llm-setup.md). App in Docker, Ollama on host: `LLM_BASE_URL=http://host.docker.internal:11434` ([Configuration – LLM](../configuration.md#llm)). |
| **Narrative shows wrong number scale** | Ensure prompt and data use correct magnitude; check app version. |

---

## Extension (PostgreSQL)

| Issue | Solution |
|-------|----------|
| **CREATE EXTENSION pgquerynarrative fails** | Copy extension files first: [PostgreSQL extension](postgres-extension.md). Local: `make install-extension`. Docker: `make install-extension-docker`. Then run `CREATE EXTENSION pgquerynarrative;` in psql. |

---

## See also

- [Configuration](../configuration.md) · [Installation](../getting-started/installation.md) · [Operations](operations.md) · [Documentation index](../index.md)
