# Installation

Prerequisites and run methods: Docker (recommended) or a local build from source.

**First time?** Prefer [Quick start](quickstart.md) (`make demo`). Use this page for
prerequisite detail, a from-source build, or wiring that isn't the guided demo.
Connecting a real database: [Connect your PostgreSQL](connect-postgres.md).

## Prerequisites

| Context | Requirements |
|---|---|
| **Docker run** | Docker and Docker Compose. No Go or PostgreSQL on the host. |
| **Local build & run** | Go 1.26+, PostgreSQL 16+ (or Docker for the database only), and CGO (`pg_query_go` is a cgo library — a C toolchain is required). |
| **Full web UI from source** | Node.js and npm to build the [React SPA](../development/setup.md). |

Optional narratives need an LLM — [LLM providers](../integrations/llm.md). The
investigation loop (findings, candidates, compare, report) works without one.

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

- **Stack:** the root [docker-compose.yml](https://github.com/pgquery-narrative/pgquerynarrative/blob/main/docker-compose.yml)
  (PostgreSQL + app), which builds the app image from the root
  [Dockerfile](https://github.com/pgquery-narrative/pgquerynarrative/blob/main/Dockerfile).
- **Endpoints:** web UI and API at **http://localhost:8080**. Health:
  [GET /health, GET /ready](../operate/monitoring.md#health-and-readiness).

For a production-shaped image and Compose overlay, see
[Deployment – Docker](../operate/deployment.md#docker).

## Local (from source)

1. **Install Go and PostgreSQL** (e.g. macOS: `brew install go postgresql@16`).
   Supported PostgreSQL major versions and what CI actually exercises:
   [Supported versions and limits](../reference/versions-limits.md).

2. **Clone and set up:**

   ```bash
   git clone https://github.com/pgquery-narrative/pgquerynarrative.git
   cd pgquerynarrative
   make setup
   make generate
   make build
   ```

   `make build` runs [build-frontend](../development/setup.md#commands) then builds
   `bin/server`.

3. **Database:** with Postgres running, create the roles/schema and run migrations:

   ```bash
   make db-init
   make migrate
   make seed
   ```

   Migrations create extensions (`pg_stat_statements`, `vector`, `hypopg`) and run
   `ALTER ROLE`, so they need a privileged role — see
   [Database roles](../security/database-roles.md).

4. **Run:** `make run` or `./bin/server`. App: **http://localhost:8080**. Verbose
   logs: `LOG_DEBUG=1 make run`.

## Verify

```bash
curl -s http://localhost:8080/ready
curl -s http://localhost:8080/api/v1/demo/scenarios | head
```

See [Health and monitoring](../operate/monitoring.md#health-and-readiness) for every
probe endpoint.

## PostgreSQL versions

The Compose Postgres image is **built**, not pulled as-is: `docker-compose.yml`
builds `tools/docker/postgres-hypopg.Dockerfile` from `POSTGRES_IMAGE`
(default `postgres:16-alpine`) plus HypoPG, tagged `pgquerynarrative-postgres:hypopg`.
To change the base, rebuild it: `POSTGRES_IMAGE=postgres:17-alpine docker compose build postgres`.
Details: [Supported versions and limits](../reference/versions-limits.md).

## See also

[Quick start](quickstart.md) · [Connect your PostgreSQL](connect-postgres.md) ·
[Configuration](../reference/configuration.md) · [Docs overview](../index.md)
