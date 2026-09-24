# Installation

Prerequisites and run methods: Docker, a release binary, or a local build from
source. For a first run, [Quick start](quickstart.md) (`make demo`) is shorter; use
this page for prerequisite detail, a from-source build, or wiring that isn't the
guided demo. Connecting a real database: [Connect your PostgreSQL](connect-postgres.md).

## Prerequisites

| Context | Requirements |
|---|---|
| **Docker run** | Docker and Docker Compose. No Go or PostgreSQL on the host. |
| **Local build & run** | Go 1.26+, PostgreSQL 16+ (or Docker for the database only), and CGO (`pg_query_go` is a cgo library, a C toolchain is required). |
| **Full web UI from source** | Node.js and npm to build the [React SPA](../development/setup.md). |

Optional narratives need an LLM, see [LLM providers](../integrations/llm.md). The
investigation loop (findings, candidates, compare, report) works without one.

## Docker

The default path for local evaluation; builds the image from source, no
pre-built pull needed. Guided demo (Postgres + app + seed):

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

### Pre-built image

```bash
VERSION=2.2.0   # replace with the release version you want
docker pull ghcr.io/pgquery-narrative/pgquerynarrative:${VERSION}
```

Images are published with an SBOM and signed with cosign:

```bash
cosign verify ghcr.io/pgquery-narrative/pgquerynarrative:${VERSION} \
  --certificate-identity-regexp 'https://github.com/pgquery-narrative/pgquerynarrative/.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

One image carries the API and the built UI. It needs a PostgreSQL to talk to, and on
a fresh database it needs `DATABASE_MIGRATION_USER`/`DATABASE_MIGRATION_PASSWORD` set
to a role that may create extensions and alter roles; the runtime query role cannot.

## Binary

Download the archive for your platform from the
[latest release](https://github.com/pgquery-narrative/pgquerynarrative/releases/latest):
`linux-amd64`, `linux-arm64`, `darwin-amd64`, `darwin-arm64`. No Docker or Go
required. Download the archive and `checksums.txt` from the same release into one
directory, then verify the checksum before extracting:

```bash
VERSION=2.2.0   # replace with the release version you downloaded
sha256sum -c checksums.txt --ignore-missing
tar -xzf pgquerynarrative-${VERSION}-linux-amd64.tar.gz
cd pgquerynarrative-${VERSION}-linux-amd64
```

Each archive, `checksums.txt`, and the SBOM are signed with cosign (Sigstore v0.3
bundles; **cosign v3 or newer required**, cosign v2 rejects them with `bundle does
not contain cert for verification`):

```bash
cosign verify-blob pgquerynarrative-${VERSION}-linux-amd64.tar.gz \
  --bundle pgquerynarrative-${VERSION}-linux-amd64.tar.gz.cosign.bundle \
  --certificate-identity-regexp 'https://github.com/pgquery-narrative/pgquerynarrative/.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

The archive is self-contained, no clone required: `bin/pgquerynarrative-server`,
`bin/pgquerynarrative-mcp`, `bin/migrate`, `bin/pqn` (the [pqn](pqn-installation.md)
terminal tool), the `pqn-extension/` PostgreSQL extension files and installer, the
built UI (`frontend/dist/`), migrations (`app/db/migrations/`), and
`config/pgquerynarrative.env.example`.

```bash
cp config/pgquerynarrative.env.example .env   # then edit the DATABASE_* values

# Migrations create extensions and ALTER ROLE, so they need a role that may do
# both, not the runtime query role, which deliberately cannot.
./bin/migrate -path app/db/migrations -database "$MIGRATION_DATABASE_URL" up
./bin/pgquerynarrative-server
```

App: **http://localhost:8080**. See [Verify](#verify) below, and
[Supported versions and limits](../reference/versions-limits.md#release-platforms)
for the full platform matrix and signing details.

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
   `ALTER ROLE`, so they need a privileged role, see
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
[Install the pqn extension](pqn-installation.md) (the terminal tool, no server) ·
[Configuration](../reference/configuration.md) · [Docs overview](../index.md)
