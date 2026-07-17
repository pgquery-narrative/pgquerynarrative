# PgQueryNarrative Roadmap

> **Mission:** Reposition this repo from "LLM narrative app with Postgres" to
> **"Postgres-native query intelligence + safe analytics, with an optional AI narrative layer."**
> Postgres is the hero: EXPLAIN, scale, RLS, partitioning, `pg_stat_statements`,
> `pg_query` validation. AI is secondary.

This file is the **durable source of truth** across Cursor sessions. Every change
to scope must update this file. Check boxes as tasks complete.

---

## How we work (session protocol)

1. Execute phases **in order**. Do not skip ahead.
2. Implement in **PR-sized slices** (1–2 hours each). One slice per session unless told otherwise.
3. Every feature needs: **implementation + how to verify + what to document** for the future case study.
4. End every session with: **Done / How to verify / Next task / Files changed**.
5. No LinkedIn posts until Phase 2–3 artifacts exist (10M-row benchmark, EXPLAIN before/after, one optimization write-up).

---

## Phase 0 — Audit & backlog  ✅ (current session)

- [x] Read README, architecture, SQL layer, validator, migrations, demo data, Docker setup
- [x] `docs/ROADMAP.md` (this file)
- [x] `docs/POSTGRES_CREDIBILITY.md` — gap analysis vs senior Postgres expectations
- [x] `docs/BACKLOG.md` — 1–2h tasks (GitHub issues blocked: `gh` token invalid)
- [x] Confirm first Phase 1 task with maintainer (B2/B3/B4: partition + seed-large)

**Audit summary:** see "Current state" at the bottom of this file.

---

## Verification gate (before B1)

**Blocked until maintainer confirms locally:**

1. Migrations apply `000018` cleanly — **Docker-only:** `make migrate-docker`; **host:** `make migrate`.
2. Seed completes — **Docker-only:** `make seed-large-docker`; **host:** `make seed-large`. `SELECT count(*) FROM demo.sales` ≈ target rows.
3. Partition pruning verified — `EXPLAIN` on a 2-month-bounded aggregation scans only recent partitions (see `docs/DATASET.md`).
4. Measured numbers pasted into `docs/DATASET.md` "Results" table.

**Do not start B1 (validator / EXPLAIN unblock) until the above is confirmed.**
Next session after confirmation: B1, then B7 (`pg_query` parser).

---

## Phase 1 — Reframe the product (2–3 weeks)

**Goal:** Prove scale + move analytics into SQL + reposition the public story.

**Decisions (this session):** range-partition `demo.sales` by month, keeping the
table name/columns (no star schema for now); support **both** Docker (documented
default) and a local Postgres via `DB_URL` override.

Acceptance criteria:

- [x] **Demo dataset schema: range-partitioned by month** (migration `000018`). _Seeding 10M rows is reproducible; capture the actual run numbers in `docs/DATASET.md`._
- [x] Reproducible seed: `make seed-large` (and small `make seed` stays for fast dev).
- [x] `docs/DATASET.md` — design, partition strategy, index rationale, measured numbers from `make seed-large-docker`.
- [x] Period comparison computed in **SQL** (window functions / `LAG` / optional materialized view); the Go `metrics` path becomes a documented fallback only.
- [x] README repositioned: lead with **secure read-only SQL + query analysis**; AI narrative is a secondary section.

File-level tasks:

- [x] `app/db/migrations/000018_partition_demo_sales.up.sql` (+ `.down.sql`) — convert `demo.sales` to monthly range partitions (PK `(id, date)`), 49 monthly partitions + `DEFAULT`, indexes on parent, readonly GRANT preserved, `demo.sales_summary` view re-created.
- [x] `tools/db/seed-large.sql` — `generate_series`-based bulk load, parameterizable `ROWS`, realistic skew.
- [x] `Makefile` — `seed-large`, `seed-large-docker`, `migrate-docker`, `postgres-up`; `ROWS` override; Docker-first docs in `docs/DATASET.md`.
- [x] `docs/DATASET.md` — written; measured numbers filled.
- [x] `app/queryrunner/` or new `app/analytics/` — SQL-based period comparison; Go fallback documented.
- [x] `README.md` — reorder sections; new positioning paragraph.

---

## Phase 2 — Postgres depth features ✅ (5/5 shipped)

**Audit:** B7–B11 verified in repo — see `docs/PHASE2_SUMMARY.md` for per-item verify commands.

| # | Backlog | Item | Status |
|---|---------|------|--------|
| 1 | B8 | EXPLAIN (JSON) integration | ☑ |
| 2 | B7 | `pg_query` parser validator | ☑ |
| 3 | B9 | `pg_stat_statements` dashboard | ☑ |
| 4 | B10 | RLS multi-tenant demo | ☑ |
| 5 | B11 | pgvector semantic search | ☑ |

1. [x] **EXPLAIN (JSON) integration (B8)** — `POST /api/v1/queries/explain`; `app/queryrunner/explain.go`; seq-scan findings + index hints.
2. [x] **`pg_query` parser validator (B7)** — `app/queryrunner/validator.go`; adversarial tests in `test/unit/app/queryrunner/queryrunner_test.go`.
3. [x] **`pg_stat_statements` dashboard (B9)** — `000019_pg_stat_statements`, `GET /api/v1/queries/stats`, UI `/stats`.
4. [x] **RLS multi-tenant demo (B10)** — `000021_sales_rls_demo`; `pgquerynarrative_sales_rep` + `SET app.current_rep`; `docs/ops/rls-demo.md`.
5. [x] **pgvector semantic search (B11)** — `app/embedding/store.go`, `GET /suggestions/similar`, `000020_pgvector_extension`, Saved Queries UI.

---

## Phase 3 — One case study

- [x] `docs/case-studies/01-query-optimization.md` with real numbers:
  - schema, indexes, `EXPLAIN (ANALYZE, BUFFERS)` before/after, tradeoffs rejected.
- [x] Headline target: "How I took a 1.1s aggregation to 145ms on 10M rows in PostgreSQL."

---

## Phase 4 — Production credibility docs

- [x] Backup/restore outline (pg_dump / PITR sketch) — `docs/ops/PRODUCTION.md` §1.
- [x] Migration strategy (expand/contract; how `golang-migrate` is used safely) — §2.
- [x] Monitoring & alerts (what to watch: bloat, long queries, replication lag) — §3.
- [x] Security model writeup: read-only role + app-layer validation, defense in depth — §4 (RLS policies; `docs/ops/rls-demo.md`).

---

## Phase 5 — LinkedIn (only after Phase 2–3 done)

- [ ] `docs/linkedin/POST_SERIES.md` — 6–8 posts, each tied to a repo artifact (diagram, EXPLAIN screenshot, benchmark). No hype; each teaches one Postgres insight.

---

## Current state (Phase 0 audit snapshot)

- **Stack:** Go 1.25, Goa v3 (`api/design` → `api/gen` + `gen`), pgx v5, golang-migrate, Docker Compose (Postgres **16-alpine** default; override `POSTGRES_IMAGE`), optional LLM providers (Ollama/Gemini/Claude/OpenAI/Groq).
- **Security base is decent:** two roles (`pgquerynarrative_app`, `pgquerynarrative_readonly`); queries run on the read-only pool; results wrapped in `SELECT * FROM (<sql>) LIMIT $1`; per-query context timeout (30s default).
- **Validator:** `pg_query_go` parse-tree walk in `app/queryrunner/validator.go` (B1/B7) — single statement, SELECT/WITH/EXPLAIN-of-SELECT, schema allowlist, nested write/DDL rejection via `disallowedASTNodes`.
- **EXPLAIN:** allowed via `extractReadOnlyQuery` unwrap (B1/B8); `POST /api/v1/queries/explain` ships plan analysis.
- **Demo data:** range-partitioned `demo.sales` (migration `000018`); `make seed-large-docker` for ~10M rows (`docs/DATASET.md`).
- **Phase 2 depth:** B7–B11 shipped including B10 RLS (`000021`, `docs/ops/rls-demo.md`; `docs/PHASE2_SUMMARY.md`).
- **Period comparison:** SQL window path in `app/queryrunner/period_comparison.go`; Go `app/metrics` documented as fallback.
- **README is Postgres-first** — secure read-only SQL, EXPLAIN, partitioned 10M-row demo; AI narrative optional.
- **Tooling:** Host may have **no Go/psql** — use `make migrate-docker` + `make seed-large-docker` (see `docs/DATASET.md`). Docker Postgres memory limit raised to **2G** for 10M-row seed.
