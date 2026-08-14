# API reference

REST API base: `http://localhost:8080/api/v1` (override with [Configuration](../configuration.md) server port). All request/response bodies are JSON. When `SECURITY_AUTH_ENABLED` is true, send `Authorization: Bearer <SECURITY_API_KEY>`; otherwise requests are unauthenticated. Rate limiting (when `SECURITY_RATE_LIMIT_RPM` > 0) returns 429. **OpenAPI 3:** `api/gen/http/openapi3.json` and `api/gen/http/openapi3.yaml` for codegen and API tooling.

Flagship flow examples: [API examples — Query Investigation](examples.md#query-investigation-flagship). Concepts: [Concepts](../concepts.md).

## Investigations

| Method | Path | Description |
|--------|------|-------------|
| POST | `/investigations` | Body: `{"title","sql", ...}`. Create investigation; runs plan analysis. Returns investigation with `explain` / findings. |
| POST | `/investigations/from-regression` | Body: `{"regression_id", ...}`. Open investigation from a regression inbox alert. |
| GET | `/investigations` | Query: `limit`, `offset`. List investigations. |
| GET | `/investigations/{id}` | Get investigation with evidence, optional candidate + comparison. |
| POST | `/investigations/{id}/suggest-rewrite` | AST-based rewrite suggestions from source SQL and plan findings. Returns `candidates[]` with `sql`, `rationale`, `category`. |
| POST | `/investigations/{id}/rank-candidates` | Body: `{"analyze": false}`. Generate rewrite + index-DDL candidates, dry-EXPLAIN, rank by cost/partitions (hypopg when available). |
| POST | `/investigations/{id}/candidate` | Body: `{"candidate_sql","analyze"}`. Attach rewrite and compare plans + equivalence (Equal / Different / Unverified). |
| POST | `/investigations/{id}/report` | Generate engineering investigation report (evidence template); requires equivalence **Equal**; sets `report_id`. |

## Workspace

| Method | Path | Description |
|--------|------|-------------|
| GET | `/workspace/overview` | Landing evidence summary (stats / attention counts). |
| GET | `/workspace/regressions` | Regression inbox items. |
| POST | `/workspace/regressions/{id}/acknowledge` | Acknowledge a regression. |
| GET | `/demo/scenarios` | Guided demo scenarios (title, problem SQL, expected improvement). No prefilled `candidate_sql`. |
| GET | `/trust` | Security & Trust snapshot for the UI. |

## Queries

| Method | Path | Description |
|--------|------|-------------|
| POST | `/queries/run` | Body: `{"sql":"...", "limit": 100, "connection_id":"default"}`. Run read-only SQL. |
| POST | `/queries/explain` | Body: `{"sql":"...", "analyze": false, "connection_id":"default"}`. Run `EXPLAIN (FORMAT JSON)`. |
| POST | `/queries/explain/compare` | Body: `{"before_sql","after_sql","analyze","connection_id"}`. Before/after plan metrics without an investigation record. |
| POST | `/queries/saved` | Body: `{"name","sql","tags","connection_id":"default"}`. Save query. |
| GET | `/queries/saved` | Query: `limit`, `offset`, `tags`, `connection_id`. List saved queries. |
| GET | `/queries/saved/{id}` | Get saved query by ID. |
| DELETE | `/queries/saved/{id}` | Delete saved query. |

## Connections

| Method | Path | Description |
|--------|------|-------------|
| GET | `/connections` | Lists configured data connections: `{"items":[{"id","name"}]}`. No secrets returned. |

## Schema

| Method | Path | Description |
|--------|------|-------------|
| GET | `/schema` | Allowed schemas, tables, columns. Query: `connection_id` (optional). |

## Suggestions

| Method | Path | Description |
|--------|------|-------------|
| GET | `/suggestions/queries` | Query: `intent`, `limit`. Suggested SQL. |
| GET | `/suggestions/similar` | Query: `text`, `limit`. Semantic similar saved queries. Requires [embeddings](../reference/semantic-search-pgvector.md). |
| POST | `/suggestions/ask` | Body: `{"question","connection_id"}`. NL → SQL → narrative. Requires [LLM](../getting-started/llm-setup.md). |
| POST | `/suggestions/explain` | Body: `{"sql"}`. Plain-English SQL explanation. Requires [LLM](../getting-started/llm-setup.md). |

## Reports

| Method | Path | Description |
|--------|------|-------------|
| POST | `/reports/generate` | Body: `{"sql","saved_query_id","connection_id"}`. **Workbench LLM narrative** report (distinct from investigation template reports). Often requires [LLM](../getting-started/llm-setup.md). |
| GET | `/reports/{id}` | Get report. |
| GET | `/reports` | Query: `limit`, `offset`, `saved_query_id`, `connection_id`. List reports. |

## Errors

Response JSON: `{"name","message","code"}`. Codes: `VALIDATION_ERROR`, `TIMEOUT_ERROR`, `NOT_FOUND`, `LLM_ERROR`, `UNAUTHORIZED`, `RATE_LIMIT_EXCEEDED`.

## See also

- [API examples](examples.md) — Investigation, compare, explain, run
- [Trust model](../trust-model.md)
- [Configuration](../configuration.md)
- [Deployment](../reference/deployment.md)
- [Embedded integration](../getting-started/embedded.md)
- [Docs overview](../index.md)
