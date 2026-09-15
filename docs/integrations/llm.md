# LLM providers

An LLM is optional. It powers natural-language and narrative features; it plays no
part in plan findings, candidate generation, compare, result verification, or
investigation reports.

## What needs an LLM, and what doesn't

| Feature | LLM? |
|---|---|
| EXPLAIN findings, `Suggest rewrite`, `Rank candidates`, `Compare`, result verification | **No** — deterministic, AST- and PostgreSQL-driven |
| Investigation report | **No** — a template over stored evidence |
| Workbench report narrative (`/reports/generate`) | Uses the LLM when configured; **falls back to a deterministic metrics narrative** if the call fails, so it degrades rather than failing outright |
| Ask (natural language → SQL → narrative) | **Yes**, required |
| Chat | **Yes**, required |
| Plain-English SQL explanation (`/suggestions/explain`) | **Yes**, required |

## Providers

Set `LLM_PROVIDER`, `LLM_MODEL`, and for cloud providers `LLM_API_KEY`
([Configuration – LLM](../reference/configuration.md#llm)).

### Ollama (local, default)

No API key. `make demo` / `make ollama-up` start Ollama in Compose and pull
`llama3.2`; the app uses `LLM_BASE_URL=http://ollama:11434`.

Host install: [ollama.ai](https://ollama.ai) or `brew install ollama`, then
`ollama serve` (`http://localhost:11434`). From the app in Docker, set
`LLM_BASE_URL=http://host.docker.internal:11434`. Other models: `mistral`,
`llama2`.

### Gemini (Google)

`LLM_PROVIDER=gemini`, `LLM_MODEL=gemini-2.0-flash`, `LLM_API_KEY=...` (key:
[Google AI Studio](https://aistudio.google.com/apikey)). 429s retry automatically.

### Claude (Anthropic)

`LLM_PROVIDER=claude`, `LLM_MODEL=claude-sonnet-4-20250514`, `LLM_API_KEY=...`
(key: [Anthropic Console](https://console.anthropic.com/)).

### OpenAI

`LLM_PROVIDER=openai`, `LLM_MODEL=gpt-4o-mini`, `LLM_API_KEY=...` (key:
[OpenAI API keys](https://platform.openai.com/api-keys)). Others: `gpt-4o`,
`gpt-4-turbo`. 429s retry automatically.

### Groq

`LLM_PROVIDER=groq`, `LLM_MODEL=llama-3.3-70b-versatile`, `LLM_API_KEY=...`
(free key: [Groq Console](https://console.groq.com/keys)). OpenAI-compatible, fast
inference. Others: `llama-3.1-8b-instant`.

## Data egress and privacy

**Ollama (`LLM_PROVIDER=ollama`) is not treated as external egress** — it's expected
to run on your own host or Docker network. Every other provider is a **cloud**
provider and is gated:

| Control | Default | Effect |
|---|---|---|
| `LLM_ALLOW_EXTERNAL_DATA` | `false` | Must be `true` before a cloud provider can be used at all; the process refuses to start otherwise |
| `LLM_SEND_ROW_DATA` | `false` | When false, prompts include SQL, column names and computed metrics, but no raw row values |
| `LLM_MAX_SAMPLE_ROWS` | `5` | Capped at 3 for cloud providers when row data is sent; capped at 5 in production regardless of provider |
| `LLM_REDACT_PII` | `true` | Redacts common PII patterns and SQL string literals before prompt construction; must stay `true` in production when a cloud provider is sent row data |
| `LLM_BUDGET_FAIL_CLOSED` | `true` for cloud providers or under StrictMode | Denies LLM calls rather than failing open when the budget ledger is unreachable |

Budgets (`LLM_DAILY_TOKEN_BUDGET`, `LLM_MONTHLY_COST_BUDGET_USD`, per-user variants,
`LLM_MAX_CALLS_PER_REPORT`) cap spend independent of the egress controls above; see
[Configuration – LLM](../reference/configuration.md#llm).

## MCP

MCP is a separate integration, not part of the LLM configuration above:
[MCP server](mcp.md).

## Troubleshooting

| Issue | Action |
|---|---|
| Connection refused (Ollama) | Start Ollama (`ollama serve`); Docker: `LLM_BASE_URL=http://host.docker.internal:11434` |
| Report fails or times out | Check provider, model and API key; see [Troubleshooting](../operate/troubleshooting.md#reports-and-llm) |
| Slow (Ollama) | Use a smaller model; first request per session is slower while the model loads |
| Cloud call rejected at startup | Set `LLM_ALLOW_EXTERNAL_DATA=true`, or switch to `ollama` |

## See also

[Configuration – LLM](../reference/configuration.md#llm) ·
[Data handling](../security/data-handling.md) · [MCP server](mcp.md) ·
[Embedded integration](embedded-go.md)
