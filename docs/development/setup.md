# Development setup

Build, test, and contribute to PgQueryNarrative. See also [Testing](testing.md) and [Contributing](https://github.com/pgquery-narrative/pgquerynarrative/blob/main/.github/CONTRIBUTING.md).

## Prerequisites

| Requirement | Purpose |
|-------------|---------|
| Go 1.25+ | Build server and run tests |
| PostgreSQL 16+ or Docker | Database (query execution, migrations, seed) |
| Git, Make | Clone and run targets |
| Node.js and npm | Build React SPA in `frontend/` for full web UI |

See [Installation](../getting-started/installation.md) for database setup.

## Setup

```bash
git clone <repository-url>
cd pgquerynarrative
make setup
make generate
```

- **Database:** Docker: `docker compose up -d postgres` then `make db-init && make migrate && make seed`. Local: same (Postgres running). See [Installation](../getting-started/installation.md).
- **Test:** `make test`

## Run locally

```bash
make run
# or
go run ./cmd/server
```

App: http://localhost:8080. Verbose logging: `LOG_DEBUG=1 make run`. The server serves the [API](../api/README.md), [health/ready](../reference/operations.md#health-checks), web export, and React SPA (from `frontend/dist/`; build with `make build-frontend` if needed).

## Workflow

1. Branch: `git checkout -b feature/name`
2. Code, test (`make test`), lint (`make lint`), format (`make fmt`)
3. Commit: [Conventional Commits](https://www.conventionalcommits.org/) (e.g. `feat: add X`)
4. After changing `api/design/*.go`: `make generate` (Goa codegen)

**Migrations:** Add `00000N_name.up.sql` and `00000N_name.down.sql` in `app/db/migrations/`; test with `make migrate`.

## Commands

| Command | Purpose |
|---------|---------|
| `make fmt` | Format code (gofmt, etc.) |
| `make lint` | Lint (golangci-lint) |
| `make test-unit` | Unit tests (`test/unit/`, `cmd/server`, `pkg/narrative`) |
| `make test-integration` | Integration tests (Docker required) |
| `make test-e2e` | E2E tests |
| `make build-frontend` | Build React SPA to `frontend/dist/` (Node/npm required) |
| `make build` | Build frontend and `bin/server` |
| `make generate` | Goa codegen (after editing `api/design/*.go`) |

## See also

- [Testing](testing.md) · [API reference](../api/README.md) · [Documentation index](../index.md) · [Contributing](https://github.com/pgquery-narrative/pgquerynarrative/blob/main/.github/CONTRIBUTING.md)
