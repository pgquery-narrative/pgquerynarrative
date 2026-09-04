# Concepts

How PgQueryNarrative thinks about query problems — the vocabulary behind the UI and API.

## What problem this solves

Teams often know a query is slow (dashboards, `pg_stat_statements`, user complaints) but lack a **repeatable path from symptom → plan evidence → verified fix → shareable write-up**. Pasting SQL into a chatbot skips the database’s own proof.

PgQueryNarrative is a **PostgreSQL investigation workbench**: safe read-only SQL, EXPLAIN analysis, **system-proposed** rewrites, before/after compare, equivalence proof, and engineering reports. An optional LLM can narrate workbench analytics; it is not required for investigation reports.

## Query Investigation

An **investigation** is a first-class workflow object. It holds:

| Piece | Meaning |
|-------|---------|
| Source SQL | The expensive or suspicious query |
| Plan evidence | Parsed `EXPLAIN` / `EXPLAIN ANALYZE` tree + findings |
| Candidate SQL | A **system-proposed** rewrite (or index-oriented alternative from Rank candidates) |
| Comparison | Side-by-side metrics (cost, time, partitions, buffers when available) |
| Equivalence | Result check: Equal / Different / Unverified |
| Report | A durable engineering artifact (evidence template, not LLM) |

Typical UI path: **Investigate** → guided scenario or paste SQL → review findings → **Suggest rewrite** or **Rank candidates** → **Compare plans** → confirm equivalence → **Generate report**.

Guided demo scenarios ship **problem SQL only** — no answer-key rewrite is prefilled.

### Visibility

Investigations are **organization-wide**. Every member of the organization can
view and act on any investigation in it — this is deliberate: an investigation
is a shared debugging record that a teammate should be able to pick up.
`created_by` records who opened it, and row-level security still confines each
investigation (and its candidate history and linked regression alerts) to its
own organization. There is no per-user "private until shared" mode.

## Rewrite engine

**Suggest rewrite** analyzes the query AST (and optional plan findings) and proposes candidates such as:

- `DATE_TRUNC` / `EXTRACT` / `to_char` / `::date` → sargable date ranges
- `COALESCE` unwraps, implicit text/numeric casts
- `OR` across columns → `UNION ALL` (when safe)
- `IN` / `NOT IN (SELECT …)` → `EXISTS` / `NOT EXISTS`

Parameterized SQL (`$1`, …) is not rewritten (fail-closed). Nothing executes automatically — a human reviews and compares.

**Rank candidates** dry-runs EXPLAIN on rewrites and projects index DDL cost via hypopg when installed; otherwise a labeled heuristic (review-only, not ranked as hypopg).

Index DDL from plan findings is **suggested only** — never auto-applied.

## What “evidence” means

Evidence is **what Postgres reported**, not model opinion:

- Plan node types (Seq Scan, Index Scan, Aggregate, …)
- Estimated cost and (when ANALYZE is on) actual time / rows
- Partition counts when the planner prunes range partitions
- App findings that name anti-patterns (e.g. function-wrapped partition key)

The product highlights those signals so a human can decide — it does not silently rewrite production SQL.

## EXPLAIN vs EXPLAIN ANALYZE

| Mode | What it does | When to use |
|------|----------------|-------------|
| `EXPLAIN` | Planner estimates only; does not execute the query body for timing | Fast triage, cheap to run |
| `EXPLAIN ANALYZE` | Executes the query and records actual times/rows | Proof of a rewrite; needs timeouts and usually a replica |

Server config gates ANALYZE (`SECURITY_EXPLAIN_ANALYZE_ENABLED`). Local demo Compose enables it so compare can show credible timings on the large seed.

## What compare proves

**Compare** runs plans for source SQL and candidate SQL and shows deltas. On the guided demo (partitioned `demo.sales`):

- Bad predicate: `DATE_TRUNC('month', date) = …` → pruning blocked → many partitions scanned
- Good predicate: `date >= … AND date < …` → pruning works → often **50 → 1** partitions on the **10M-row seed** (`make demo-bootstrap`)

So “verified rewrite” means: **the database plan changed in the expected way**, measured by Postgres — not that an LLM preferred the new SQL.

## Equivalence

After compare, the app checks whether both queries return the same results (`COUNT(*)` plus an order-independent sample). Status is **Equal**, **Different**, or **Unverified** (run errors stay Unverified, never Different). **Generate report** requires Equal.

## Two report types

| Type | Path | Content |
|------|------|---------|
| **Investigation report** | Investigate workflow | Evidence template from plans, SQL, and comparison |
| **Workbench LLM report** | Query runner / Ask | LLM narrative from metrics and query results |

## Regression inbox

The workspace can surface queries that look worse over time (from stats / polling). That is an **entry point** into investigation, not a separate product. On default `make demo`, the inbox is empty unless real `pg_stat_statements` data exists. Set **`APP_ENV=demo`** for seeded demo alerts and inflated KPIs. You still land in the same evidence → suggest → compare → report loop.

## Schema allowlist and demo data

By default, user SQL may only touch schemas listed in `DATABASE_ALLOWED_SCHEMAS` (default `demo`). The bundled `demo.sales` table is range-partitioned by month so partition-pruning stories are reproducible. See [Dataset](DATASET.md) and [Trust model](trust-model.md).

## Optional LLM layer

Narratives and “Ask in natural language” use a configured provider (often local Ollama in demo). Investigation reports remain useful **without** an LLM when they are built from plan metrics and SQL. See [LLM setup](getting-started/llm-setup.md).

## See also

- [Trust model](trust-model.md) — what the app will and will not do
- [API examples](api/examples.md) — investigation create → suggest-rewrite → compare → report
- [UI overview](ui-overview.md) — page map
