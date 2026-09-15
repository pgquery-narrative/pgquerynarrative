# CLI

Command-line access to the REST API. The app must already be running
([Quick start](../getting-started/quickstart.md)). The CLI runs in a container
(`make cli`) or as the host script (`tools/cli/pgquerynarrative-cli.sh`); either
way it calls the same [REST API](../integrations/rest-api.md) the web UI uses.

## Commands

| Command | Description | API equivalent |
|---|---|---|
| `make cli CMD='query "SQL"'` | Run a read-only query. Optional limit: `query "SQL" 10` | `POST /queries/run` |
| `make cli CMD='list'` | List saved queries | `GET /queries/saved` |
| `make cli CMD='get "uuid"'` | Get a saved query | `GET /queries/saved/{id}` |
| `make cli CMD='save "Name" "SQL"'` | Save a query. Optional tags: `"tags,a,b"` | `POST /queries/saved` |
| `make cli CMD='report "SQL"'` | Generate a workbench narrative report | `POST /reports/generate` |

Interactive: `make cli-shell`, then `pgquerynarrative query "SELECT * FROM demo.sales LIMIT 5"`
(alias `pqn`).

```bash
make cli CMD='query "SELECT product_category, SUM(total_amount) FROM demo.sales GROUP BY product_category"'
```

## Environment

| Variable | Default | Description |
|---|---|---|
| `PGQUERYNARRATIVE_API_URL` | `http://app:8080` | API base URL. Use `http://localhost:8080` if you run the script directly on the host rather than via `make cli` |
| `PGQUERYNARRATIVE_FORMAT` | `table` | `table` or `json` |

**Quoting:** quote SQL in the outer command:
`make cli CMD='query "SELECT * FROM demo.sales"'`. For a single quote inside SQL,
use `'\''`.

## Known limitations

- **No authentication support.** The CLI sends no `Authorization` header and has no
  API-key variable — every command fails with 401 once
  `SECURITY_AUTH_ENABLED=true`. Use `curl` with a Bearer token, or the
  [REST API](../integrations/rest-api.md) directly, against an authenticated server.
- **No `--connection-id` flag.** For a non-default connection, use the REST API with
  `connection_id`, or an [MCP tool](../integrations/mcp.md) that accepts it.
- **`make cli` always runs in a container** — there is no separate host-binary path
  today, despite `PGQUERYNARRATIVE_API_URL`'s default suggesting one exists.

## See also

[REST API](../integrations/rest-api.md) · [API reference](api.md) ·
[MCP server](../integrations/mcp.md)
