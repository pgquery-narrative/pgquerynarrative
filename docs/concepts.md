# Concepts

How PgQueryNarrative thinks about query problems — the vocabulary behind the UI, GIF, and API.

## What problem this solves

Teams often know a query is slow (dashboards, `pg_stat_statements`, user complaints) but lack a **repeatable path from symptom → plan evidence → verified fix → shareable write-up**. Pasting SQL into a chatbot skips the database’s own proof.

PgQueryNarrative is a **PostgreSQL investigation workbench**: safe read-only SQL, EXPLAIN analysis, before/after compare, and engineering reports. An optional LLM can narrate; it is not required for the core loop.

## Query Investigation

An **investigation** is a first-class workflow object. It holds:

| Piece | Meaning |
|-------|---------|
| Source SQL | The expensive or suspicious query |
| Plan evidence | Parsed `EXPLAIN` / `EXPLAIN ANALYZE` tree + findings |
| Candidate SQL | A proposed rewrite (or index-oriented alternative) |
| Comparison | Side-by-side metrics (cost, time, partitions, buffers when available) |
| Report | A durable engineering artifact you can share |

Typical UI path: **Investigate** → guided scenario or paste SQL → review findings → **Compare plans** → **Generate report**.

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
- Good predicate: `date >= … AND date < …` → pruning works → often **50 → 1** partitions on the 10M-row seed

So “verified rewrite” means: **the database plan changed in the expected way**, measured by Postgres — not that an LLM preferred the new SQL.

## Regression inbox

The workspace can surface queries that look worse over time (from stats / polling). That is an **entry point** into investigation, not a separate product. You still land in the same evidence → compare → report loop.

## Schema allowlist and demo data

By default, user SQL may only touch schemas listed in `DATABASE_ALLOWED_SCHEMAS` (default `demo`). The bundled `demo.sales` table is range-partitioned by month so partition-pruning stories are reproducible. See [Dataset](DATASET.md) and [Trust model](trust-model.md).

## Optional LLM layer

Narratives and “Ask in natural language” use a configured provider (often local Ollama in demo). Investigation reports remain useful **without** an LLM when they are built from plan metrics and SQL. See [LLM setup](getting-started/llm-setup.md).

## See also

- [Trust model](trust-model.md) — what the app will and will not do
- [API examples](api/examples.md) — investigation create → candidate → report
