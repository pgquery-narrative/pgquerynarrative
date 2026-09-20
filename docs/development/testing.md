# Testing

Canonical commands, then a QA matrix for the guarantees this product makes. Makefile
and CI are the source of truth here — where a `make` target runs more than a single
`go test` command, this page says so rather than presenting them as equivalent.

## Canonical commands

| What | Command | Notes |
|---|---|---|
| Unit | `make test-unit` | Runs `go test` across a specific package list (`test/unit/...`, `app/auth`, `app/queryrunner`, `app/service`, `app/security`, `app/llm`, `app/audit`, `app/story`, `cmd/server`, `pkg/narrative`, `app/embedding`, `app/config`, `app/metrics`, `web`) — **not** a bare `go test ./...`, which would miss in-package tests these packages hold alongside their code |
| Integration | `make test-integration` | `test/integration/...`, real Postgres via testcontainers. Needs Docker |
| E2E | `make test-e2e` | `test/e2e/...`, full HTTP API against real Postgres |
| Everything above | `make test` | = `test-unit` + `test-integration` |
| Migration cycle | `make migrate-cycle-docker` | up → down -all → up, proves migrations reversible |
| DB security | `make db-security-verify-docker` | `tools/db/verify_security.sh` — the read-only boundary |
| Helm StrictMode | `make helm-strict-check` | Renders the chart and checks production gates without a cluster |
| Frontend unit | `make test-frontend` | `cd frontend && npm test` (Vitest) |
| Frontend typecheck / lint | `cd frontend && npm run typecheck` / `npm run lint` | |
| Browser E2E | `make test-playwright` | Playwright, no OIDC. `make test-playwright-oidc` runs the OIDC-flow variant |
| Release/image smoke | CI only: `Release build smoke`, `Docker image smoke` | Not a local `make` target — see `.github/workflows/ci.yml` and `release.yml` |
| `pqn` extension | `make verify-pqn-extension` | Throwaway PostgreSQL primary and hot standby: ownership, `PUBLIC`, analyst limits, the ledger, a real 1.0 → 1.1 upgrade. `PG_IMAGE=postgres:16` (or 17, 18) picks the version. Needs Docker |
| `pqn` tool | `make verify-pqn-cli` | The tool against a slow-query lab: `top`, `investigate`, a wrong rewrite is `Different`, the limits hold. Needs Docker |
| REST-calling extension | `make verify-extension` | Upgrade path, `PUBLIC` grants, the URL lock, the API key. Needs Docker |
| `pqn` docs | `make verify-pqn-docs` | Runs the `bash` blocks of the pqn quick start and installation guide as written. Needs Docker |
| `pqn` pitch | `make verify-pqn-pitch` | The core pitch against independent oracles on a 17-million-row database: every proposal, wrong rewrites, time zones, concurrent writes, limits, access, the ledger. About five minutes and 2 GB of disk. Needs Docker and Python 3 |
| Docs | `make docs-check` | `mkdocs build --strict` |
| Docs contract | `make docs-contract-check` | Config/API/error/vocabulary/link checks against the code — see `tools/docscheck` |
| External links | `make docs-links` | lychee, needs network |

`go test ./test/unit/... ./cmd/server/... ./pkg/narrative/... -v` covers most of
`test-unit` but omits the in-package suites in `app/auth`, `app/queryrunner`, etc. —
use `make test-unit` for the real coverage set, or run a single package directly:

```bash
go test ./test/unit/app/queryrunner/... -v
go test ./test/unit/app/service/... -run TestBuildPerfSuggestions_LimitApplied -v
```

## Test layout

| Package | What is tested |
|---|---|
| `test/unit/app/queryrunner` | SQL validation (schema, SELECT-only, disallowed keywords) |
| `test/unit/app/catalog` | Schema/catalog loader |
| `test/unit/app/charts` | Chart suggestions |
| `test/unit/app/metrics` | Period comparison, trend, anomalies, data quality |
| `test/unit/app/llm` | Narrative prompt builder |
| `test/unit/app/story` | Narrative sanitizer |
| `test/unit/app/service` | Perf suggestions, metrics-to-API conversion |
| `test/unit/app/suggestions` | Query suggestions (curated, limit) |
| `test/unit/app/ratelimit`, `app/errors`, `app/db`, `app/auth`, `app/security`, `pkg/narrative` | As named |
| `test/unit/web` | Report export |
| `app/queryrunner` (in-package) | Rewrite rules (`rewriter_test.go`, `rewriter_patterns_test.go`, `rewriter_param_test.go`, `rewriter_antijoin_test.go`) |
| `test/integration` | Query runner and rewrite equivalence against real Postgres, investigation candidates, regression detection/poller/multiconnection, schedule multi-replica, migration roundtrip, audit modes, multi-org security, OIDC staging, managed-key authorization, embeddings, pilot acceptance |
| `test/e2e` | Full HTTP API: queries, schema, suggestions, reports |

## QA matrix — product guarantees

| Guarantee | Where it's tested |
|---|---|
| Plain `EXPLAIN` never executes the query | `test/integration/p0_hero_path_test.go`, `rewrite_equivalence_test.go` (`TestInvestigationCreate_IsEstimateOnly`) |
| `EXPLAIN ANALYZE` executes and requires the flag + permission | `test/unit/app/security`, `test/integration/security_hardening_test.go` |
| Result verification requires the `query` permission | `TestComparePlans_VerifyResultsRequiresQueryPermission` |
| All five equivalence states behave as documented | `test/integration/rewrite_equivalence_test.go` |
| Rewrite semantics (fail-closed cases) | `app/queryrunner/rewriter_*_test.go`, `rewrite_equivalence_test.go` |
| Cross-organization isolation | `test/integration/multi_org_security_test.go` |
| Multi-connection regression detection | `test/integration/regression_multiconnection_test.go` |
| Fix lifecycle transitions | `test/unit/app/service`, `test/integration/investigation_candidates_test.go` |
| Schedule runner is safe across replicas | `test/integration/schedule_multireplica_test.go`, CI job `Schedule multi-replica` |
| Security & Trust endpoint reflects real per-connection state | `test/integration/security_hardening_test.go` |
| Read-only DB boundary holds at the privilege level | `tools/db/verify_security.sh`, CI job `DB security verify` |

## Manual checks

**Auth, rate limiting, audit** — start with security enabled to actually exercise
them (defaults are off):

```bash
SECURITY_AUTH_ENABLED=true SECURITY_API_KEY=test-key SECURITY_RATE_LIMIT_RPM=5 make run
```

```bash
curl -s -o /dev/null -w "%{http_code}\n" http://localhost:8080/api/v1/queries/saved                # 401
curl -s -o /dev/null -w "%{http_code}\n" -H "Authorization: Bearer test-key" http://localhost:8080/api/v1/queries/saved  # 200
curl -s -o /dev/null -w "%{http_code}\n" http://localhost:8080/health                                # 200, always unprotected
for i in $(seq 8); do curl -s -o /dev/null -w "%{http_code}\n" -H "Authorization: Bearer test-key" http://localhost:8080/api/v1/queries/saved; done  # 6th+ = 429
```

Audit log: `psql -d pgquerynarrative -c "SELECT event_type, details, user_id FROM app.audit_logs ORDER BY created_at DESC LIMIT 10;"`

**Analytics** — run a time-series query (`tools/db/testing-queries.sql`), confirm
`metrics.time_series.<measure>` includes a forecast and confidence interval, and
that a query with ≥ 2 numeric measures and ≥ 10 rows produces `metrics.correlations`.
Automated coverage: `test/unit/app/metrics`.

## See also

[Development setup](setup.md) · [Repository architecture](repository.md) ·
[REST API examples](../integrations/rest-api.md)
