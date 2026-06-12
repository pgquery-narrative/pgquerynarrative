# Semantic search (pgvector)

Saved queries and reports can be found by **meaning**, not just substring match. The app embeds
text with an external model (Ollama `nomic-embed-text` by default), stores vectors in Postgres,
and runs **k-nearest-neighbor** search with the **pgvector** extension when available.

This is a **Postgres depth** feature: similarity ranking happens in the database via
`embedding_vector <=> $query` (cosine distance) on an **HNSW** index—not in application memory.

## End-to-end flow (saved queries)

```
┌─────────────┐     embed (Ollama)      ┌──────────────────────────────┐
│ Save query  │ ───────────────────────►│ app.query_embeddings         │
│ POST /saved │                         │  · embedding jsonb (always)  │
└─────────────┘                         │  · embedding_vector vector   │
                                        │    (768) + HNSW when pgvector│
┌─────────────┐     embed query text    └──────────────┬───────────────┘
│ Similar     │ ───────────────────────────────────────┤
│ GET /similar│                                        │
└─────────────┘                         ORDER BY embedding_vector <=> $1
                                        LIMIT k  (Postgres k-NN)
```

1. **Save** — `POST /api/v1/queries/saved` persists SQL in `app.saved_queries`.
2. **Embed** — `app/service/queries.go` calls `Embedder.Embed(name + description + sql)` and
   `embedding.Store.Upsert` → `app.query_embeddings` (JSONB + optional `vector(768)`).
3. **Search** — `GET /api/v1/suggestions/similar?text=…` embeds the search text, then
   `Store.FindSimilar` runs pgvector SQL (or in-memory cosine fallback if extension/column missing).

Reports follow the same pattern via `app.report_embeddings` and `GET /api/v1/reports/similar`.

## Where the code lives

| Piece | Location |
|-------|----------|
| Embed on save | `app/service/queries.go` (`Save`) |
| pgvector k-NN | `app/embedding/store.go` (`FindSimilar`, `FindSimilarReports`) |
| Similar API | `app/suggestions/suggestions.go` (`Similar`) |
| Tables | `000005_query_embeddings`, `000007_pgvector_embeddings`, `000013_report_embeddings`, `000020_pgvector_extension` |
| UI | Saved Queries (`/saved`) semantic search; Reports (`/reports`) similar search |

## Enabling pgvector

`vector` does **not** need `shared_preload_libraries`—only `CREATE EXTENSION`.

### Docker (recommended for B11 verify)

```bash
# Image includes pgvector binaries
POSTGRES_IMAGE=pgvector/pgvector:pg16 docker compose up -d postgres
make migrate-docker   # 000020 creates extension + HNSW columns when available
```

Stock `postgres:16-alpine` works without pgvector; the app falls back to **in-memory** cosine
similarity over JSONB embeddings (fine for dev, not the Postgres story).

### Host Postgres

1. Install pgvector for your PG version (`apt install postgresql-16-pgvector`, Homebrew, etc.).
2. `./tools/db/ensure-pgvector-extension.sh` or `CREATE EXTENSION IF NOT EXISTS vector;`
3. `make migrate` (migrations `000007` / `000020` add `embedding_vector` + HNSW).

## Embeddings config

Set `EMBEDDING_BASE_URL` and `EMBEDDING_MODEL` (see [Configuration – Embeddings](../configuration.md)).
When unset, save still works but **no vectors** are stored and `/suggestions/similar` returns `[]`.

With Docker + Ollama on the host:

```env
LLM_PROVIDER=ollama
EMBEDDING_BASE_URL=http://host.docker.internal:11434
EMBEDDING_MODEL=nomic-embed-text
```

Settings UI (`/settings`) shows whether embeddings are enabled.

## Verify (curl)

Requires embeddings enabled and at least two saved queries with different SQL shapes.

```bash
# 1. Save two analytics queries
curl -s -X POST http://localhost:8080/api/v1/queries/saved \
  -H "Content-Type: application/json" \
  -d '{"name":"Sales by region","sql":"SELECT region, SUM(total_amount) FROM demo.sales GROUP BY region","tags":["rollup"]}'

curl -s -X POST http://localhost:8080/api/v1/queries/saved \
  -H "Content-Type: application/json" \
  -d '{"name":"Sales by category","sql":"SELECT product_category, COUNT(*) FROM demo.sales GROUP BY product_category","tags":["rollup"]}'

# 2. Semantic search (region-shaped question should rank region query first)
curl -s 'http://localhost:8080/api/v1/suggestions/similar?text=revenue%20by%20region&limit=5' | jq .

# 3. Confirm pgvector column populated (pgvector image + migrate)
docker compose exec -T postgres psql -U postgres -d pgquerynarrative -c \
  "SELECT sq.name, qe.embedding_vector IS NOT NULL AS has_vector
   FROM app.query_embeddings qe
   JOIN app.saved_queries sq ON sq.id = qe.saved_query_id
   LIMIT 5;"
```

**EXPLAIN the k-NN plan** (when `has_vector` is true):

```sql
EXPLAIN SELECT sq.name, (1 - (qe.embedding_vector <=> '[0.1,0.2,...]'::vector(768))) AS score
FROM app.query_embeddings qe
JOIN app.saved_queries sq ON sq.id = qe.saved_query_id
WHERE qe.embedding_vector IS NOT NULL
ORDER BY qe.embedding_vector <=> '[0.1,0.2,...]'::vector(768)
LIMIT 5;
```

Expect an **HNSW index scan** on `idx_query_embeddings_vector_cosine` for non-trivial row counts.

## Fallback behavior

| Condition | Search path |
|-----------|-------------|
| `pgvector` + `embedding_vector` populated | `ORDER BY embedding_vector <=> $1` (HNSW) |
| JSONB only or pgvector missing | Load rows, cosine similarity in Go (`findSimilarInMemory`) |

New saves always attempt both JSONB and `vector(768)`; upsert falls back to JSONB-only on error.

## See also

- [API examples](../api/examples.md) — curl for `/suggestions/similar`
- [Configuration](../configuration.md) — Embeddings variables
- [Troubleshooting](troubleshooting.md)
