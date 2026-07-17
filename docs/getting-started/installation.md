# Installation

Prerequisites and run methods for PgQueryNarrative: Docker (recommended) or local build from source.

## Prerequisites

| Context | Requirements |
|---------|--------------|
| **Docker run** | Docker and Docker Compose. No Go or PostgreSQL on host. |
| **Local build & run** | Go 1.25+, PostgreSQL 16+ (or Docker for DB only). |
| **Full web UI from source** | Node.js and npm (to build the [React SPA](../development/setup.md)). |

Report generation requires an LLM. See [LLM setup](llm-setup.md) and [Configuration – LLM](../configuration.md#llm).

## Docker (recommended)

Single command starts PostgreSQL, runs migrations and seed, then the application.

```bash
git clone <repository-url>
cd pgquerynarrative
make start-docker
```

- **Stack:** Root [docker-compose.yml](../../docker-compose.yml) (PostgreSQL + app). App image from root [Dockerfile](../../Dockerfile).
- **Endpoints:** Web UI (React SPA) and API at **http://localhost:8080**. Health: [GET /health](../reference/operations.md#health-checks), [GET /ready](../reference/operations.md#health-checks).

For production-style image and Compose, see [Deployment – Docker](../reference/deployment.md#docker).

## Local (from source)

1. **Install Go and PostgreSQL** (e.g. macOS: `brew install go postgresql@18`). Supported PostgreSQL: 16, 17, 18.

2. **Clone and setup:**
   ```bash
   git clone <repository-url>
   cd pgquerynarrative
   make setup
   make generate
   make build
   ```
   `make build` runs [build-frontend](../development/setup.md#commands) then builds `bin/server`.

3. **Database:** With Postgres running, create DB/roles and run migrations:
   ```bash
   make db-init
   make migrate
   make seed
   ```

4. **Run:** `make run` or `./bin/server`. App: **http://localhost:8080**. Verbose logs: `LOG_DEBUG=1 make run`.

## Verify

- **Readiness (DB):** `curl -s http://localhost:8080/ready`
- **API:** `curl http://localhost:8080/api/v1/queries/saved`

See [Operations – Health checks](../reference/operations.md#health-checks) for probe endpoints.

## PostgreSQL versions

Supported: 16, 17, 18. Docker default image: `postgres:18-alpine`. Override: `POSTGRES_IMAGE=postgres:17-alpine make start-docker`.

## See also

- [Quick start](quickstart.md) · [Configuration](../configuration.md) · [Deployment](../reference/deployment.md) · [Documentation index](../README.md)
