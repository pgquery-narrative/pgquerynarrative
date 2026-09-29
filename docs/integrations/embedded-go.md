# Embedded Go

Use PgQueryNarrative inside your own Go service: construct a `narrative.Client`
directly, or mount its HTTP endpoints on your own router. Configuration matches the
[standalone server](../getting-started/installation.md); see
[Configuration](../reference/configuration.md).

The Go module path is intentionally unchanged:
`github.com/pgquerynarrative/pgquerynarrative`, even though the GitHub
organization is `pgquery-narrative`. Import paths below are correct as written.

## Library usage

```go
import (
    "github.com/pgquerynarrative/pgquerynarrative/pkg/narrative"
)

cfg := narrative.LoadConfig()
client, err := narrative.NewClient(ctx, cfg)
if err != nil { /* ... */ }
defer client.Close()

result, err := client.RunQuery(ctx, "SELECT ... FROM demo.sales", 100)
report, err := client.GenerateReport(ctx, sql)
schema, err := client.GetSchema(ctx)
```

Example: [`examples/library-usage/basic.go`](https://github.com/pgquery-narrative/pgquerynarrative/blob/main/examples/library-usage/basic.go).

Beyond the four methods above, the client also exposes `ListSavedQueries`,
`GetSavedQuery`, `SaveQuery`, `DeleteSavedQuery`, `GenerateReportFromSaved`,
`RunQueryWithOptions`, health/readiness (`Ready`, `HealthReport`), and typed service
accessors (`QueriesService`, `ReportsService`, `SchemaService`,
`SuggestionsService`, `ConnectionsService`, `DashboardsService`,
`SchedulesService`, `InvestigationsService`, `WorkspaceService`) for the rest of the
API surface.

## HTTP middleware (Chi, Gin, Echo)

Package: [`pkg/narrative/middleware`](https://github.com/pgquery-narrative/pgquerynarrative/tree/main/pkg/narrative/middleware).

| Framework | Mount call |
|---|---|
| Chi | `narrativemw.MountChi(r, client, "/api")` |
| Chi (with auth) | `narrativemw.MountChiSecured(r, client, "/api", sec)` |
| Gin | `narrativemw.MountGin(r, client, "/api")` |
| Echo | `narrativemw.MountEcho(e, client, "/api")` |

For auth and rate-limit parity with the standalone server, build a
`narrativemw.SecurityConfig` using `narrativemw.NewAuthenticator(...)` and
`narrativemw.NewSessionManager(...)` (the underlying auth types live under `internal/`
and can't be constructed directly outside this module), plus `client.AuditStore()` and
your own rate limiter, then use `MountChiSecured` (Chi) or wrap individual handlers
with `WrapSecured` (Gin, Echo; there is no secured mount helper for those two). Leaving
`Authenticator` nil disables authentication entirely, so don't skip it.

For multi-tenant deployments, an API key or OIDC principal with no explicit `org_id`
needs organization membership resolution, and CLI/MCP-issued keys need durable storage;
both live behind the same `internal/` boundary, so build them with
`narrativemw.NewMembershipStore(pool, autoJoinDefault)` and
`narrativemw.NewManagedKeyStore(pool)`, then attach with
`authenticator.SetMembershipStore(...)` / `.SetManagedKeyStore(...)` before building
`SecurityConfig`. OIDC-based auth itself still isn't exposed to embedders (see above).

Mounted routes (with prefix `/api`; use `""` to mount at root):

| Method | Path | Description |
|---|---|---|
| POST | `/api/query/run` | Body: `{"sql":"...", "limit": N}`. Run read-only SQL |
| POST | `/api/report/generate` | Body: `{"sql":"..."}`. Generate a workbench narrative report ([LLM](llm.md), with a deterministic fallback) |
| GET | `/api/schema` | Allowed schemas, tables, columns |
| GET | `/api/suggestions/queries` | Query: `intent`, `limit`. Suggested SQL |

!!! warning "Auth applies by prefix, not by mount call"
    `AuthMiddleware` only requires authentication for paths starting with `/api/`.
    If you mount at a prefix other than `/api` or `""`, `MountChiSecured` will still
    serve those routes **without** requiring auth. Choose `/api` (or `""` with your
    own `/api`-rooted router) if you use the secured mount.

## See also

[Configuration](../reference/configuration.md) · [REST API](rest-api.md) ·
[Releases and versioning](../project/releases.md#pkgnarrative-api-stability): what's stable here
