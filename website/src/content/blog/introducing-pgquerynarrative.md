---
title: "Introducing PgQueryNarrative"
description: "A PostgreSQL query investigation tool that proposes bounded changes and requires measurement before calling anything improved."
date: 2026-09-30
author: "PgQueryNarrative"
tags: ["announcement", "postgresql"]
draft: true
---

Most slow-query workflows stop at the execution plan: read `EXPLAIN`, form a theory, try a rewrite, judge by feel whether it's faster. PgQueryNarrative closes that loop with evidence instead of a feeling, and the evidence it insists on is result verification, not just a faster-looking plan.

Point it at a slow query and it runs `EXPLAIN`, optionally `ANALYZE`, and flags concrete plan problems: sequential scans, spills to disk, partition pruning defeated by a wrapped column. When a rewrite or an index applies, it proposes one, drawn from the query's own parse tree rather than a model guessing at SQL; most queries only get plan findings, no candidate at all. The candidate is compared against the original with a real `EXPLAIN (ANALYZE)` run on both sides, so the comparison is measured rather than estimated. Asked to, it runs both queries again and checks whether they return the same rows, and reports which of three things happened: a full checksum match, a bounded sample match, or that the check couldn't run. It always says which, rather than staying quiet about it. All of it lands in a report: the plan findings, the candidate, the comparison, the verification result. A person reads that and decides what to apply. PgQueryNarrative doesn't run `CREATE INDEX` or alter a query in production on its own.

## Estimate, measurement, verification

A planner cost estimate is a guess about work, expressed in arbitrary units (useful for comparing plans against each other, useless as a time). A rewrite existing says nothing about whether it's actually faster; only a measured `EXPLAIN (ANALYZE)` run establishes that. And a faster measured run still says nothing about whether it returns the same rows. That's a separate, explicit check, and skipping it means the report just says verification wasn't requested. Most tuning advice collapses all of this into one number, which is exactly why it's hard to trust. PgQueryNarrative keeps the states apart and names them: proposed, estimated, measured, verified.

## What the rule set covers today

The rule set covers a handful of shapes so far: DATE_TRUNC and EXTRACT wraps, a couple of cast forms, COALESCE, OR and IN restructurings. Each one got added because a real query hit that pattern, and it only shipped once it was provable against the parser's own AST, not because it looked like it would generalize.

This post will go stale before the code does. For what's actually covered right now, check the repo instead of this page.
