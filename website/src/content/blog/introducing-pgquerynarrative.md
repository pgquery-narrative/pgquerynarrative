---
title: "Introducing PgQueryNarrative"
description: "A PostgreSQL query investigation tool that proposes bounded changes and requires measurement before calling anything improved."
date: 2026-09-30
author: "PgQueryNarrative"
tags: ["announcement", "postgresql"]
draft: true
---

Most slow-query workflows end at the execution plan. You read the `EXPLAIN` output, you form a theory, you try a rewrite, and you eyeball whether it feels faster. PgQueryNarrative tries to close that loop with evidence instead of a feeling.

## What it does

Given a slow query, PgQueryNarrative:

1. Runs `EXPLAIN`, optionally with `ANALYZE`, and flags concrete plan problems: sequential scans, spills to disk, partition pruning defeated by a wrapped column.
2. Proposes a bounded rewrite or index from the query's own parse tree, when one applies. Not every query gets a candidate.
3. Compares the original and candidate query with a real `EXPLAIN (ANALYZE)` run on both sides, not planner cost alone.
4. Checks whether the candidate returns the same rows as the original, and says so explicitly: a full checksum match, a bounded sample match, or that verification did not happen.
5. Writes the plan findings, the candidate, the comparison, and the verification result into a report.

A person decides whether to apply the change. PgQueryNarrative does not run `CREATE INDEX` or alter a query in production itself.

## Why the distinction between estimate and measurement matters

A planner cost estimate is not a time. A candidate rewrite existing is not evidence it is faster. A faster measured run is not evidence it returns the same rows. Collapsing these into one number is exactly what makes a lot of tuning advice hard to trust, so PgQueryNarrative keeps them as separate, named states instead: proposed, estimated, measured, verified.

## What is out of scope, on purpose

This is not a general query optimizer, and it is not an autonomous one. The rewrite rules are a fixed, auditable set (unwrapping a wrapped date column, for example), not a language model guessing at SQL. Index suggestions are surfaced for review, not applied. Result verification can be skipped, but the report says so rather than staying silent about it.

## Where this is going

The rewrite rule set is still small, and it is meant to grow the same way it started: one rule at a time, each one provable against the query's own syntax, not against a hope that it generalizes.

The project is open source and the code is the actual specification. If the wording above ever drifts from what the tool does, the code is right and this post is wrong.
