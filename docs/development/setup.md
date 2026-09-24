# Development setup

Build, test, and contribute to PgQueryNarrative. See also
[Testing](testing.md) and
[Contributing](https://github.com/pgquery-narrative/pgquerynarrative/blob/main/.github/CONTRIBUTING.md).

## Prerequisites

| Requirement | Purpose |
|---|---|
| Go 1.26+, CGO toolchain | Build the server and run tests (`pg_query_go` is a cgo library) |
| PostgreSQL 16+, or Docker | Database: query execution, migrations, seed |
| Git, Make | Clone and run targets |
| Node.js 22+, npm | Build the React SPA in `frontend/` |

Database setup: [Installation](../getting-started/installation.md).

## Setup

```bash
git clone https://github.com/pgquery-narrative/pgquerynarrative.git
cd pgquerynarrative
make setup
make generate
```

- **Database:** `docker compose up -d postgres` then `make db-init && make migrate && make seed`
- **Test:** `make test`

## Run locally

```bash
make run
# or
go run ./cmd/server
```

App: http://localhost:8080. Verbose logging: `LOG_DEBUG=1 make run`. The server
serves the [API](../reference/api.md), [health/ready](../operate/monitoring.md#health-and-readiness),
report export, and the React SPA (`frontend/dist/`; `make build-frontend` to
rebuild).

## Workflow

1. Branch: `git checkout -b feature/name`
2. Code, test (`make test`), lint (`make lint`), format (`make fmt`)
3. Commit: [Conventional Commits](https://www.conventionalcommits.org/) (`feat: ...`, `fix: ...`)
4. After changing `api/design/*.go`: `make generate` (Goa codegen, see
   [Repository architecture](repository.md#code-generation))
5. Changed a public config default, API shape, or error code? Update the matching
   [Reference](../reference/configuration.md) page in the same change, see
   [Change workflows](change-workflows.md); `make docs-contract-check` enforces it

**Migrations:** add `00000N_name.up.sql` and `.down.sql` in `app/db/migrations/`;
test with `make migrate` and `make migrate-cycle-docker`.

## Commands

| Command | Purpose |
|---|---|
| `make fmt` | Format code (gofmt, etc.) |
| `make lint` | Lint (golangci-lint) |
| `make test-unit` | Unit tests |
| `make test-integration` | Integration tests (Docker required) |
| `make test-e2e` | End-to-end tests |
| `make build-frontend` | Build the SPA to `frontend/dist/` |
| `make build` | Build frontend and `bin/server` |
| `make generate` | Goa codegen after editing `api/design/*.go` |
| `make docs` / `make docs-check` | Preview / strictly build the documentation site |
| `make docs-contract-check` | Check documented facts against the code |

## See also

[Repository architecture](repository.md) · [Testing](testing.md) ·
[Documentation index](../index.md) ·
[Contributing](https://github.com/pgquery-narrative/pgquerynarrative/blob/main/.github/CONTRIBUTING.md)
