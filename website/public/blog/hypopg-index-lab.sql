-- Companion lab: Test a PostgreSQL Index Before You Build It with HypoPG
-- Run once in a fresh, disposable database with the HypoPG package installed:
--   psql -X -v ON_ERROR_STOP=1 -d your_scratch_database -f hypopg-index-lab.sql
-- Creates 500,000 synthetic orders, then a real index in the final phase.
-- Keep the whole script in one connection; hypothetical indexes are session-local.
-- Tested with PostgreSQL 16.15 and HypoPG 1.4.3.

\set ON_ERROR_STOP on
\pset pager off

CREATE EXTENSION IF NOT EXISTS hypopg;
CREATE SCHEMA index_lab;
CREATE TABLE index_lab.orders (
    order_id bigint PRIMARY KEY,
    account_id integer NOT NULL,
    placed_at date NOT NULL,
    total_cents integer NOT NULL,
    note text NOT NULL
);

-- Each of 1,000 accounts has one order per day for 500 days.
-- The note supplies row width; it is not selected by the investigated query.
INSERT INTO index_lab.orders
SELECT n + 1,
       (n % 1000) + 1,
       DATE '2025-01-01' + (n / 1000),
       1000 + (n % 50000),
       repeat('x', 120)
FROM generate_series(0, 499999) AS g(n);

ANALYZE index_lab.orders;

-- Simplify the example plans. This is a lab setting, not tuning advice.
SET max_parallel_workers_per_gather = 0;

SELECT version();
SELECT extversion AS hypopg_version FROM pg_extension WHERE extname = 'hypopg';
SELECT name, setting, unit
FROM pg_settings
WHERE name IN ('shared_buffers', 'work_mem', 'random_page_cost',
               'effective_cache_size', 'max_parallel_workers_per_gather', 'jit')
ORDER BY name;

CREATE TEMP TABLE captured_plans (
    sample_id integer GENERATED ALWAYS AS IDENTITY,
    phase text NOT NULL,
    document jsonb NOT NULL
);

-- Store the actual EXPLAIN JSON without copying the query into every phase.
CREATE FUNCTION pg_temp.capture_plan(phase_name text, execute_query boolean)
RETURNS void LANGUAGE plpgsql AS $function$
DECLARE
    plan_json jsonb;
    explain_prefix text;
    investigated_query constant text := $query$
        SELECT order_id, total_cents
        FROM index_lab.orders
        WHERE account_id = 42
          AND placed_at >= DATE '2026-03-01'
          AND placed_at < DATE '2026-04-01'
    $query$;
BEGIN
    explain_prefix := CASE WHEN execute_query
        THEN 'EXPLAIN (ANALYZE, BUFFERS, TIMING OFF, FORMAT JSON) '
        ELSE 'EXPLAIN (FORMAT JSON) '
    END;
    EXECUTE explain_prefix || investigated_query INTO plan_json;
    INSERT INTO captured_plans (phase, document) VALUES (phase_name, plan_json);
END;
$function$;

SELECT hypopg_reset();
SELECT pg_temp.capture_plan('baseline_estimate', false);

SELECT * FROM hypopg_create_index(
    'CREATE INDEX ON index_lab.orders (account_id)'
);
SELECT pg_temp.capture_plan('account_only_estimate', false);

SELECT hypopg_reset();
SELECT * FROM hypopg_create_index(
    'CREATE INDEX ON index_lab.orders (account_id, placed_at)'
);
SELECT pg_temp.capture_plan('composite_estimate', false);

-- This executes the SELECT. The hypothetical index cannot serve the scan.
SELECT pg_temp.capture_plan('hypothetical_analyze', true);
SELECT hypopg_reset();

-- One warm-up, then five measured runs without a supporting real index.
SELECT pg_temp.capture_plan('baseline_warmup', true);
DO $block$
BEGIN
    FOR i IN 1..5 LOOP
        PERFORM pg_temp.capture_plan('baseline_run', true);
    END LOOP;
END;
$block$;

-- This statement builds a REAL index, only on the disposable lab table.
CREATE INDEX orders_account_placed_idx
    ON index_lab.orders (account_id, placed_at);

-- One warm-up, then five measured runs with the real index.
SELECT pg_temp.capture_plan('real_index_warmup', true);
DO $block$
BEGIN
    FOR i IN 1..5 LOOP
        PERFORM pg_temp.capture_plan('real_index_run', true);
    END LOOP;
END;
$block$;

-- Plan costs are estimates in arbitrary units. Execution time is milliseconds.
SELECT sample_id, phase,
       document #>> '{0,Plan,Node Type}' AS root_node,
       document #>> '{0,Plan,Total Cost}' AS estimated_total_cost,
       document #>> '{0,Plan,Plan Rows}' AS estimated_rows,
       document #>> '{0,Plan,Actual Rows}' AS actual_rows,
       document #>> '{0,Execution Time}' AS execution_ms,
       document #>> '{0,Plan,Shared Hit Blocks}' AS shared_hit_blocks,
       document #>> '{0,Plan,Shared Read Blocks}' AS shared_read_blocks
FROM captured_plans
ORDER BY sample_id;

SELECT phase,
       count(*) AS runs,
       min((document #>> '{0,Execution Time}')::numeric) AS min_ms,
       percentile_cont(0.5) WITHIN GROUP (
           ORDER BY (document #>> '{0,Execution Time}')::double precision
       ) AS median_ms,
       max((document #>> '{0,Execution Time}')::numeric) AS max_ms
FROM captured_plans
WHERE phase IN ('baseline_run', 'real_index_run')
GROUP BY phase
ORDER BY phase;

SELECT count(*) AS matching_rows
FROM index_lab.orders
WHERE account_id = 42
  AND placed_at >= DATE '2026-03-01'
  AND placed_at < DATE '2026-04-01';

SELECT pg_size_pretty(pg_relation_size('index_lab.orders')) AS table_size,
       pg_size_pretty(pg_relation_size('index_lab.orders_account_placed_idx')) AS real_index_size;

-- Full plan evidence, including both conditions in the composite Index Cond.
SELECT phase, jsonb_pretty(document) AS plan
FROM captured_plans
WHERE phase IN ('baseline_estimate', 'account_only_estimate',
                'composite_estimate', 'hypothetical_analyze', 'real_index_warmup')
ORDER BY sample_id;

SELECT hypopg_reset();
RESET max_parallel_workers_per_gather;
-- The index_lab schema remains for inspection; discard the scratch database when done.
