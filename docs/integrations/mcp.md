# MCP server

An MCP (Model Context Protocol) server that lets an AI client (Claude Desktop,
Cursor) call a subset of the REST API as tools. It is a thin client of the running
server; it contains no query logic of its own and needs the app running.

## What it is

- **Binary:** `bin/mcp-server`, built with `make build-mcp`
- **Transport:** stdio only (no HTTP mode)
- **Authentication to the app:** `PGQUERYNARRATIVE_API_KEY`, sent as
  `Authorization: Bearer <key>` on every call; set it to the same value as
  `SECURITY_API_KEY` when the server has auth enabled
- **Target URL:** `PGQUERYNARRATIVE_URL`, default `http://localhost:8080`
- **Safety limits:** the target host is locked to prevent SSRF via a malicious URL,
  every call has a 60-second timeout, and responses are capped at 1 MiB

## Configuring a client

```json
{
  "mcpServers": {
    "pgquerynarrative": {
      "command": "/path/to/pgquerynarrative/bin/mcp-server",
      "env": {
        "PGQUERYNARRATIVE_URL": "http://localhost:8080",
        "PGQUERYNARRATIVE_API_KEY": "your-secret-key"
      }
    }
  }
}
```

Claude Desktop config path: macOS
`~/Library/Application Support/Claude/claude_desktop_config.json`, Windows
`%APPDATA%\Claude\`, Linux `~/.config/Claude/`. Cursor:
`.cursor/mcp.json` in the project root, or Settings → MCP. Template:
`config/mcp-example.json`. Restart the client after editing.

## Tool surface

| Tool | Calls | Notes |
|---|---|---|
| `run_query` | `POST /queries/run` | `sql`, `limit`, `connection_id` |
| `explain_sql` | `POST /suggestions/explain` | Plain-English explanation of a query (LLM) |
| `list_schemas` / `get_schema` | `GET /schema` | `connection_id` |
| `get_context` | `GET /schema` + `GET /queries/saved` | `connection_id` applies only to the schema call, not the saved-query list |
| `list_connections` | `GET /connections` | - |
| `suggest_queries` | `GET /suggestions/queries` | `intent`, `limit` |
| `ask_question` | `POST /suggestions/ask` | Natural language → SQL → narrative (LLM) |
| `generate_report` | `POST /reports/generate` | Workbench narrative report (LLM, with deterministic fallback) |
| `list_saved_queries` | `GET /queries/saved` | `limit`, `offset`, `connection_id` |
| `list_reports` / `get_report` | `GET /reports` / `GET /reports/{id}` | `limit`, `offset`, `connection_id` |

All accept optional `connection_id` where relevant, see
[Multiple connections](../workflows/connections.md).

## What is not supported

There are no MCP tools for investigations, EXPLAIN/EXPLAIN ANALYZE, plan compare,
result verification, `pg_stat_statements` / regressions, dashboards, schedules, share
links, or chat. The investigation workflow ([Investigate a slow query](../workflows/investigate.md)
onward) is REST- and UI-only today; use the [REST API](rest-api.md) directly for it.

## Troubleshooting

| Symptom | Cause |
|---|---|
| Connection refused | The app isn't running at `PGQUERYNARRATIVE_URL` |
| `401` in the tool error | Auth is on server-side and `PGQUERYNARRATIVE_API_KEY` is missing or wrong |
| Tool call rejected before any HTTP request | `PGQUERYNARRATIVE_URL` points at a host the SSRF lock refuses |

The exact error (`API POST /api/v1/queries/run: 401`, `connection refused`, …) is
shown in the chat's tool result.

## See also

[LLM providers](llm.md) · [REST API](rest-api.md) · [Configuration reference](../reference/configuration.md#mcp)
