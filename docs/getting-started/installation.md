# Installation

Prerequisites and run methods for PgQueryNarrative: Docker (recommended) or local build from source.

**First time?** Prefer [Quick start](quickstart.md) (`make demo`) — it is the supported guided path. Use this page when you need prerequisites detail, a from-source build, or non-demo wiring. Connecting a real database: [Connect your PostgreSQL](connect-postgres.md).

## Prerequisites

| Context | Requirements |
|---------|--------------|
| **Docker run** | Docker and Docker Compose. No Go or PostgreSQL on host. |
| **Local build & run** | Go 1.25+, PostgreSQL 16+ (or Docker for DB only). |
| **Full web UI from source** | Node.js and npm (to build the [React SPA](../development/setup.md)). |

Optional narratives require an LLM — [LLM setup](llm-setup.md). Investigation compare/report work without one.

## Docker (recommended)

Guided demo (Postgres + app + seed):

```bash
git clone https://github.com/pgquery-narrative/pgquerynarrative.git
cd pgquerynarrative
make demo
```

Compose without the demo helper:

```bash
make start-docker
```

- **Stack:** Root [docker-compose.yml](../../docker-compose.yml) (PostgreSQL + app). App image from root [Dockerfile](../../Dockerfile).
- **Endpoints:** Web UI and API at **http://localhost:8080**. Health: [GET /health](../reference/operations.md#health-checks), [GET /ready](../reference/operations.md#health-checks).

For production-style image and Compose, see [Deployment – Docker](../reference/deployment.md#docker).

## Local (from source)

1. **Install Go and PostgreSQL** (e.g. macOS: `brew install go postgresql@18`). Supported PostgreSQL: 16, 17, 18.

2. **Clone and setup:**
   ```bash
   git clone https://github.com/pgquery-narrative/pgquerynarrative.git
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
- **API:** `curl -s http://localhost:8080/api/v1/demo/scenarios | head`

See [Operations – Health checks](../reference/operations.md#health-checks) for probe endpoints.

## PostgreSQL versions


Supported: 16, 17, 18. Docker default image: `postgres:18-alpine`. Override: `POSTGRES_IMAGE=postgres:17-alpine make start-docker`.

## See also

- [Quick start](quickstart.md) · [Connect your PostgreSQL](connect-postgres.md) · [Configuration](../configuration.md) · [Docs overview](../index.md)
