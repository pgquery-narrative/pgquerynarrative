-- Large, reproducible seed for demo.sales (run AFTER migrations).
--
-- Row count is parameterizable:
--   psql "$DB_URL" -v rows=10000000 -f tools/db/seed-large.sql
-- or via the Makefile:
--   make seed-large ROWS=10000000
--
-- Data is intentionally skewed (not uniform) so aggregations and EXPLAIN plans
-- behave like real workloads:
--   - product_category is front-weighted (Electronics most common) via power(random(), 2)
--   - dates spread across ~24 months so rows land in many monthly partitions
--   - total_amount is consistent: unit_price * quantity

\set ON_ERROR_STOP on
\timing on

\if :{?rows}
\else
\set rows 10000000
\endif

\echo Seeding demo.sales with :rows rows (this can take a few minutes at 10M)...

INSERT INTO demo.sales (date, product_category, product_name, quantity, unit_price, total_amount, region, sales_rep)
SELECT
    (CURRENT_DATE - (random() * 730)::int)                                                              AS date,
    (ARRAY['Electronics','Furniture','Office Supplies','Clothing','Accessories'])[1 + floor(power(random(), 2) * 5)::int] AS product_category,
    (ARRAY['Alpha','Beta','Gamma','Delta','Epsilon','Zeta'])[1 + (random() * 5)::int]                   AS product_name,
    g.quantity,
    g.unit_price,
    ROUND(g.unit_price * g.quantity, 2)                                                                 AS total_amount,
    (ARRAY['North','South','East','West','Central'])[1 + (random() * 4)::int]                           AS region,
    (ARRAY['A. Lee','B. Singh','C. Patel','D. Kim','E. Garcia'])[1 + (random() * 4)::int]               AS sales_rep
FROM (
    SELECT
        (1 + (random() * 19)::int)                  AS quantity,
        ROUND((10 + random() * 490)::numeric, 2)    AS unit_price
    FROM generate_series(1, :rows)
) g;

ANALYZE demo.sales;

\echo Done. Row count:
SELECT count(*) AS demo_sales_rows FROM demo.sales;
