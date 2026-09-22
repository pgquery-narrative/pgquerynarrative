BEGIN;

-- Serializes the empty-table check below against a concurrent run of this
-- same script (e.g. docker-start's Step 4 and the Playwright runner's
-- seed_demo() overlapping against the same volume). Without this, two
-- sessions can both evaluate WHERE NOT EXISTS before either commits and each
-- insert their own 300,000 rows — reproduced by hand: two concurrent runs
-- against an empty table produced 600,000 rows, the exact duplication this
-- script exists to prevent. The lock is transaction-scoped (released at
-- COMMIT/ROLLBACK) and held across both the check and the insert, so the
-- second session only proceeds once the first has committed and its own
-- check correctly sees the now-existing rows.
-- hashtextextended(..., 0), not hashtext(): matches the advisory-lock key
-- convention already established in app/llm/budget.go and
-- app/service/regression_poller.go (64-bit key space, no implicit cast to
-- bigint needed).
SELECT pg_advisory_xact_lock(hashtextextended('pgquerynarrative.demo.sales.seed', 0));

-- Dates: last 365 days from CURRENT_DATE (rolling window). Guided Investigate
-- scenarios read live MIN(date)/MAX(date) and inject DATE literals so sample
-- SQL returns rows without freezing a calendar year.

CREATE SCHEMA IF NOT EXISTS demo;

INSERT INTO demo.sales (
    id,
    date,
    product_category,
    product_name,
    quantity,
    unit_price,
    total_amount,
    region,
    sales_rep
)
SELECT
    gen_random_uuid(),
    (CURRENT_DATE - (random() * 365)::int),
    (ARRAY['Electronics','Furniture','Office Supplies','Clothing','Accessories'])[1 + (random() * 4)::int],
    (ARRAY['Alpha','Beta','Gamma','Delta','Epsilon','Zeta'])[1 + (random() * 5)::int],
    1 + (random() * 20)::int,
    ROUND((10 + random() * 490)::numeric, 2),
    ROUND((10 + random() * 490)::numeric, 2) * (1 + (random() * 20)::int),
    (ARRAY['North','South','East','West','Central'])[1 + (random() * 4)::int],
    (ARRAY['A. Lee','B. Singh','C. Patel','D. Kim','E. Garcia'])[1 + (random() * 4)::int]
-- 300k rows across the monthly partitions (~55 MB, ~3s to insert).
--
-- 8000 rows was too small to demonstrate anything: at ~160 rows per partition
-- the whole table sits in shared buffers, partition pruning saves microseconds,
-- and run-to-run timing noise exceeded the difference being shown — the same
-- query measured 2ms and 6ms on consecutive runs. At this size the demo
-- comparison reports 24ms -> 7ms, which is a difference a reader can trust.
--
-- For the 10M-row benchmark used in the case study, use `make seed-large-docker`.
--
-- This script is invoked unconditionally by both `docker-start` (Step 4) and
-- the Playwright runner's seed_demo() every time either runs — with no guard,
-- re-running `make demo` or the Playwright suite against an already-seeded
-- volume silently added another 300k rows on top of what was there (a live
-- stack went 300k -> 900k across one QA session from three ordinary re-runs).
-- WHERE NOT EXISTS makes this idempotent: skip entirely once demo.sales has
-- any row, rather than truncating — a prior `make seed-large-docker` run
-- should not be silently wiped back down to 300k by a later plain `make demo`.
FROM generate_series(1, 300000)
WHERE NOT EXISTS (SELECT 1 FROM demo.sales LIMIT 1);

COMMIT;
