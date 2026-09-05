# LLM setup

Optional **narrative** and **Ask** flows require an LLM. **Query Investigation** (plan findings, compare, engineering report from evidence) works without one.

Supported providers: **Ollama** (local), **Gemini**, **Claude**, **OpenAI**, **Groq**. Set [Configuration – LLM](../configuration.md#llm) variables: `LLM_PROVIDER`, `LLM_MODEL`, and for cloud providers `LLM_API_KEY`.

## Ollama (local)

No API key. **Docker Compose (default):** `make demo` or `make ollama-up` starts in-compose Ollama and pulls `llama3.2`. App uses `LLM_BASE_URL=http://ollama:11434`.

**Host install (optional):** [ollama.ai](https://ollama.ai) or `brew install ollama` (macOS). Start: `ollama serve` (API: http://localhost:11434). Set `LLM_BASE_URL=http://host.docker.internal:11434` when the app runs in Docker.

- **Model:** `llama3.2` (default). Others: `mistral`, `llama2`. Verify in compose: `docker compose exec ollama ollama list`.

## Gemini (Google)

- **API key:** [Google AI Studio](https://aistudio.google.com/app/apikey).
- **Config:** `LLM_PROVIDER=gemini`, `LLM_MODEL=gemini-2.0-flash`, `LLM_API_KEY=your_key`. On 404 try another model; on 429 the client retries.

## Claude (Anthropic)

- **API key:** [Anthropic Console](https://console.anthropic.com/). Paid.
- **Config:** `LLM_PROVIDER=claude`, `LLM_MODEL=claude-3-5-sonnet-20241022`, `LLM_API_KEY=your_key`. [Anthropic models](https://docs.anthropic.com/en/docs/about-claude/models).

## OpenAI (GPT)

- **API key:** [OpenAI API keys](https://platform.openai.com/api-keys).
- **Config:** `LLM_PROVIDER=openai`, `LLM_MODEL=gpt-4o-mini`, `LLM_API_KEY=your_key`. Others: `gpt-4o`, `gpt-4-turbo`. On 429 the client retries.

## Groq

- **API key:** [Groq Console](https://console.groq.com/keys). OpenAI-compatible; fast inference.
- **Config:** `LLM_PROVIDER=groq`, `LLM_MODEL=llama-3.3-70b-versatile`, `LLM_API_KEY=your_key`. Others: `llama-3.1-8b-instant`, `mixtral-8x7b-32768`.

## MCP (Claude desktop / Cursor) {#mcp-claude-desktop--cursor}

Use PgQueryNarrative as tools (run query, generate report, list saved/reports) from Claude or Cursor.

1. Run app and build MCP: `make build-mcp` → `bin/mcp-server`.
2. Edit your client’s MCP config: [Configuration – MCP](../configuration.md#mcp-claude-desktop--cursor).
3. Add the `pgquerynarrative` server; **command** = path to `bin/mcp-server`. Example: `config/mcp-example.json`; copy the `pgquerynarrative` block into `mcpServers`.
4. Restart the client. Then use the tools in chat (see [How to use MCP tools in Cursor / Claude](#how-to-use-mcp-tools-in-cursor--claude)).

### How to use MCP tools in Cursor / Claude {#how-to-use-mcp-tools-in-cursor--claude}

Once the MCP server is connected, the AI can call tools when you ask. You don’t run the tools yourself; you ask in natural language and the client invokes the right tool(s).

**In Cursor**

1. **Start the app** (so the MCP server can call it):  
   `./bin/server` or `make start-local` (app must be running at the URL the MCP server uses, default `http://localhost:8080`).
2. **Open a Cursor chat** (e.g. Composer or Chat).
3. **Ask in plain language.** Example: *“What were the top 5 products by revenue?”* → uses `ask_question` (NL→SQL + report). Other tools: `list_schemas`, `get_schema`, `run_query`, `explain_sql`, `suggest_queries`, `generate_report`.
4. The model will call the right tool and show you the result. If the app isn’t running or the MCP server path is wrong, you’ll see a connection or tool error.

**In Claude desktop**

Same idea: add the `pgquerynarrative` server to your MCP config (see [Configuration – MCP](../configuration.md#mcp-claude-desktop--cursor)), restart Claude, then in a conversation ask things like “What can I query?” or “What were the top 5 products by revenue?” Claude will use `list_schemas` / `ask_question` etc. as needed.

**Tool summary**

| Tool | Use when |
|------|----------|
| `list_schemas` / `get_schema` | See allowed schemas, tables, columns. |
| `get_context` | Schema + saved queries in one call. |
| `suggest_queries` | Get example or intent-matched SQL suggestions. |
| `ask_question` | Ask in natural language → get SQL + narrative report. |
| `run_query` | Execute read-only SQL, get rows. |
| `generate_report` | Run SQL and get narrative report. |
| `explain_sql` | Get a short plain-English explanation of a query. |
| `list_saved_queries` / `list_reports` / `get_report` | List or fetch saved data. |

## Troubleshooting

| Issue | Action |
|-------|--------|
| **Connection refused (Ollama)** | Start Ollama (`ollama serve`). Docker: `LLM_BASE_URL=http://host.docker.internal:11434`. Ensure a model is pulled. |
| **Report fails / timeout** | Check provider and API key. [Troubleshooting – Reports and LLM](../reference/troubleshooting.md#reports-and-llm). |
| **Slow (Ollama)** | Use a smaller model (e.g. `mistral`); first run per session is slower. |
| **Query tool fail (MCP)** | See [MCP tool fails](#mcp-query-tool-fail) below. |

### MCP query tool fail

When Cursor/Claude reports that a query tool (e.g. `run_query`, `ask_question`) failed:

1. **App not running** — The MCP server calls the PgQueryNarrative HTTP API. Start the app first: `./bin/server` or `make start-local`. It must be reachable at the URL the MCP server uses (default `http://localhost:8080`).
2. **Wrong URL** — If the app runs on another port or host, set the URL in the MCP config: `"env": { "PGQUERYNARRATIVE_URL": "http://localhost:YOUR_PORT" }`.
3. **Auth enabled** — If the server is started with `SECURITY_AUTH_ENABLED=true`, the API requires a Bearer token. Add the same key to the MCP server env: `"env": { "PGQUERYNARRATIVE_API_KEY": "your-secret-key" }` in the `pgquerynarrative` server block. Rebuild the MCP server after adding env support (`make build-mcp`).

The exact error (e.g. `API POST /api/v1/queries/run: 401` or `connection refused`) is usually shown in the tool result in the chat; use it to see which of the above applies.

## See also

- [Configuration](../configuration.md) — All LLM and MCP variables
- [Quick start](quickstart.md) — Get the app running
- [Deployment](../reference/deployment.md) — Docker/Helm (e.g. LLM_BASE_URL in containers)
- [Embedded integration](embedded.md) — Library and middleware (same config)
- [Troubleshooting](../reference/troubleshooting.md) — Report and connection issues
- [Documentation index](../index.md)
