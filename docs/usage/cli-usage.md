# CLI usage

Command-line access to the PgQueryNarrative API. The app must be running ([Quick start](../getting-started/quickstart.md): `make start-docker` or `make start-local`). The CLI runs in a container (Docker) or on the host (local binary). It calls the same [REST API](../api/README.md) used by the web UI.

## Multi-connection note

The legacy `make cli` wrapper does not yet expose a dedicated `--connection-id` flag. For connection-specific automation, use:

- REST API calls with `connection_id` (see [API examples](../api/examples.md)), or
- MCP tools (`run_query`, `generate_report`, `list_saved_queries`, `list_reports`, `get_schema`, `list_schemas`, `ask_question`) with optional `connection_id`.

## Commands

| Command | Description | API equivalent |
|---------|-------------|----------------|
| `make cli CMD='query "SQL"'` | Run read-only query. Optional limit: `query "SQL" 10`. | POST /api/v1/queries/run ([Queries](../api/README.md#queries)) |
| `make cli CMD='list'` | List saved queries. | GET /api/v1/queries/saved |
| `make cli CMD='get "uuid"'` | Get saved query by ID. | GET /api/v1/queries/saved/{id} |
| `make cli CMD='save "Name" "SQL"'` | Save query. Optional tags: `"tags,a,b"`. | POST /api/v1/queries/saved |
| `make cli CMD='report "SQL"'` | Generate narrative report. Requires [LLM](../getting-started/llm-setup.md). | POST /api/v1/reports/generate |

**Interactive:** `make cli-shell` then e.g. `pgquerynarrative query "SELECT * FROM demo.sales LIMIT 5"` or alias `pqn list`.

## Example

```bash
make cli CMD='query "SELECT product_category, SUM(total_amount) FROM demo.sales GROUP BY product_category"'
```

## Environment (CLI)

| Variable | Default | Description |
|----------|---------|-------------|
| `PGQUERYNARRATIVE_API_URL` | `http://app:8080` | API base URL. Use `http://localhost:8080` when CLI runs on host (e.g. `make cli` with local server). |
| `PGQUERYNARRATIVE_FORMAT` | `table` | Output: `table` or `json`. |

JSON output: `PGQUERYNARRATIVE_FORMAT=json make cli CMD='query "SELECT 1"'` (when CLI on host).

**Quoting:** Quote SQL in the outer command: `make cli CMD='query "SELECT * FROM demo.sales"'`. For single quotes inside SQL: `'\''`.

## See also

- [API example](../api/examples.md) · [API reference](../api/README.md) · [Documentation index](../index.md)
