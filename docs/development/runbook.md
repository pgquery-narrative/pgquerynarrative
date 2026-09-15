# Developer runbook

Day-to-day commands. For architecture see [Repository architecture](repository.md);
for product vocabulary see [Concepts](../concepts.md).

## Daily dev

| Step | Command |
|---|---|
| Start DB (Docker) | `make start-docker` → http://localhost:8080 |
| Or local Postgres | `make start-local` |
| App only | `make run` (port 8080, `demo` schema) |
| Stop | `make stop` (Docker) or Ctrl+C (local) |

## Before committing

```bash
make fmt
make lint
make test
```

After editing `api/design/*.go`: `make generate`.

## Tests

| What | Command |
|---|---|
| Unit | `make test-unit` |
| One package | `go test ./test/unit/app/metrics/... -v` |
| Integration (Docker) | `make test-integration` |
| E2E | `make test-e2e` |
| Full suite | `make test` |

Full command list and what each guarantees: [Testing](testing.md).

## Build

| What | Command |
|---|---|
| Binary + frontend | `make build` → `bin/server`, `frontend/dist/` |
| Frontend only | `make build-frontend` |
| MCP server | `make build-mcp` → `bin/mcp-server` |

## Database

| What | Command |
|---|---|
| Init (Docker) | `make db-init` |
| Migrate | `make migrate` |
| Seed demo | `make seed` |
| Example queries | `tools/db/testing-queries.sql` |

## Quick API checks

```bash
curl -s http://localhost:8080/health
curl -s http://localhost:8080/api/v1/demo/scenarios | jq '.items[0]'
curl -s -X POST http://localhost:8080/api/v1/queries/run \
  -H "Content-Type: application/json" -d '{"sql":"SELECT 1 AS n","limit":10}'
```

## Analytics sanity check

Run a time-series query from `tools/db/testing-queries.sql`, confirm
`metrics.time_series.<measure>` includes `next_period_forecast`,
`forecast_ci_lower`, `forecast_ci_upper`. Full steps: [Testing — manual checks](testing.md#manual-checks).

## Troubleshooting

| Issue | Try |
|---|---|
| Port in use | `PORT=8081 make run` |
| DB connection failed | Check `DATABASE_*` env; ensure Postgres is up and migrated |
| Integration tests skip | Docker required |
| Goa / gen out of sync | `make generate` after any `api/design/*.go` change |

## See also

[Development setup](setup.md) · [Testing](testing.md) · [Configuration reference](../reference/configuration.md)
