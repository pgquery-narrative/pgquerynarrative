# Backlog

1–2 hour, PR-sized tasks. GitHub issues are not used here because `gh` is
authenticated with an invalid token in this environment; treat this file as the
issue tracker. Each task lists **Acceptance**, **Verify**, and **Doc** (what to
capture for the future case study).

Status: ☐ todo · ◐ in progress · ☑ done

---

## P0 — Unblock Postgres depth

### B1 ☑ Allow `EXPLAIN` + kill substring false positives in validator
- **Why:** EXPLAIN is blocked and `SELECT created_at …` is wrongly rejected. Blocks Phase 2 #1.
- **Acceptance:** Validator permits `EXPLAIN` / `EXPLAIN (FORMAT JSON…)` of an otherwise-valid read-only query; no longer rejects identifiers containing `create`/`alter`/`analyze`/`copy`; single-statement + schema rules preserved.
- **Verify:** `make test` (unit) green; new test cases for `created_at` and `EXPLAIN (FORMAT JSON) SELECT …`.
- **Doc:** False-positive class fixed by AST walk (`pg_query` parse tree) instead of substring `Contains`; `EXPLAIN` unwraps inner `SelectStmt` before schema/write checks. Sets up B7/B8.

---

## Phase 1 — Reframe

### B2 ☑ Range-partition `demo.sales` by month
- **Acceptance:** Migration `000018_partition_demo_sales` creates a partitioned `demo.sales` (range on `date`, monthly) with per-partition indexes; existing API/queries still work; readonly GRANTs preserved; `demo.sales_summary` view re-created on the new table.
- **Verify:** `EXPLAIN SELECT … WHERE date >= …` shows partition pruning; `\d+ demo.sales` lists partitions.
- **Doc:** Partition key choice + pruning evidence for `docs/DATASET.md`.

### B3 ☑ `make seed-large` / `make seed-large-docker` — 10M-row realistic load
- **Acceptance:** `tools/db/seed-large.sql` bulk-loads ~10M rows with realistic skew; `ROWS` parameterizable; Docker path needs no host psql; small `make seed` unchanged.
- **Verify:** `make seed-large-docker` (or `make seed-large`) completes; `SELECT count(*) FROM demo.sales` ≈ target.
- **Doc:** Load time + method (single INSERT … SELECT vs batched) in `docs/DATASET.md`.

### B4 ☑ `docs/DATASET.md` (design done; fill measured numbers after a run)
- **Acceptance:** Documents row count, load time, `pg_total_relation_size` per table + indexes, partition layout, and index rationale.
- **Verify:** Numbers match a fresh `make seed-large`.
- **Doc:** This *is* the doc; reused by the case study.

### B5 ☑ Period comparison in SQL (window functions)
- **Acceptance:** Period-over-period uses `LAG`/`DATE_TRUNC` in SQL; Go `app/metrics` path kept as documented fallback only.
- **Verify:** Same numbers as current Go path on the example query; integration test.
- **Doc:** Before/after (Go-in-memory vs SQL) note for the SQL-fluency story.

### B6 ☑ Reposition README (Postgres-first)
- **Acceptance:** README leads with secure read-only SQL + query analysis; AI narrative demoted to a secondary section. No repo rename.
- **Verify:** Maintainer review.
- **Doc:** n/a.

---

## Phase 2 — Depth (B7–B11; audit: 5/5 ☑ — see `docs/PHASE2_SUMMARY.md`)

### B7 ☑ Replace regex validator with `pg_query_go` parser
- **Acceptance:** Parse tree drives read-only + single-statement + schema enforcement; same `Validator` interface; `validator_test.go` green + new cases.
- **Verify:** Adversarial cases (comments, casing, dollar-quoting) handled; benign identifiers pass.
- **Doc:** Security writeup input (Phase 4). Already implemented in B1 (`pg_query.ParseToJSON` + AST walk); this session added adversarial unit cases in `test/unit/app/queryrunner/queryrunner_test.go` (no regex/substring blocklist remains).

### B8 ☑ EXPLAIN (JSON) integration
- **Acceptance:** `POST /api/v1/queries/explain` runs `EXPLAIN (FORMAT JSON[, ANALYZE, BUFFERS])`; `app/queryrunner/explain.go` parses plan tree, flags seq scans / high-cost nodes, suggests indexes.
- **Verify:** `make postgres-up` (+ app on :8080). Seq-scan query:
  `curl -s -X POST http://localhost:8080/api/v1/queries/explain -H "Content-Type: application/json" -d '{"sql":"SELECT product_category, SUM(total_amount) FROM demo.sales WHERE region = '\''North'\'' GROUP BY product_category"}' | jq '.findings[] | select(.is_seq_scan) | .message'`
  — expect ≥1 message containing `Sequential scan` and `btree index`. `"analyze": true` path returns same finding shape (executes query). `make test` green (`explain_test.go`, integration).
- **Doc:** Core of case study #1 (before/after plans). Verified example + expected JSON in `docs/api/examples.md`. Dockerfile pins `goa@v3.24.1` so `make start-docker` builds on Go 1.25.

### B9 ☑ `pg_stat_statements` dashboard
- **Acceptance:** Extension enabled (`shared_preload_libraries` in Docker); read-only endpoint + minimal UI for top queries by total/mean time, calls, rows.
- **Verify:** `make postgres-recreate && make migrate-docker && make start-docker`. Run queries via API, then:
  `curl -s 'http://localhost:8080/api/v1/queries/stats?order_by=total_time&limit=10' | jq '.items[:3] | .[] | {query: .query[0:80], calls, total_time_ms}'`
  — expect wrapped `SELECT * FROM (…) AS pgqn_sub` statements after `POST /queries/run`. UI: **Query Stats** at `/stats`.
- **Doc:** `docs/ops/PRODUCTION.md` §3 (`pg_stat_statements`); migration `000019_pg_stat_statements`; `make postgres-recreate` after Compose config change.

### B10 ☑ RLS multi-tenant demo
- **Acceptance:** RLS policies so a `sales_rep` session sees only its own rows; demo role + `SET app.current_rep`.
- **Verify:** Two sessions, two reps, disjoint result sets; policy shown in `\d demo.sales`. Migration `000021_sales_rls_demo`; API path uses `pgquerynarrative_readonly` permissive policy (unchanged visibility).
- **Doc:** `docs/ops/rls-demo.md`; `docs/ops/PRODUCTION.md` §4 (real policies + role/path table).

### B11 ☑ pgvector on saved queries/reports (already satisfied + documented)
- **Acceptance:** Only if it tells a real story; semantic search over saved queries.
- **Verify:** Save two queries with embeddings on; `GET /api/v1/suggestions/similar?text=…` ranks the closer SQL first; `embedding_vector` populated with `POSTGRES_IMAGE=pgvector/pgvector:pg16`. UI: Saved Queries semantic search; Reports similar search (pre-existing). `make test` — `test/integration/embedding_similar_test.go`.
- **Doc:** `docs/reference/semantic-search-pgvector.md` (end-to-end flow); `docs/api/examples.md`; migration `000020_pgvector_extension` (idempotent extension + HNSW when image supports vector).

---

## Phase 3–5 (placeholders; do not start early)

- B12 ☑ `docs/case-studies/01-query-optimization.md` (after B2–B4, B8).
- B13 ☑ Phase 4 ops docs (backup/restore, expand/contract migrations, monitoring, security model).
- B14 ☑ `docs/linkedin/POST_SERIES.md` (only after Phase 2–3 artifacts exist).
