# Examples

- **Library:** `library-usage/` — use PgQueryNarrative as a Go library (run query, optional report). Requires PostgreSQL and optionally LLM.
- **Embedded:** `gin-integration/`, `echo-integration/`, `chi-integration/` — minimal HTTP servers with narrative endpoints. Build: `go build -o bin/example-gin ./examples/gin-integration` (same for echo, chi).

See [docs overview](../docs/index.md), [Embedded Go](../docs/integrations/embedded-go.md),
and [REST API examples](../docs/integrations/rest-api.md).
