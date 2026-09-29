-- Convert demo.sales into a monthly RANGE-partitioned table.
--
-- Why: the demo dataset is being scaled to millions of rows. Range-partitioning
-- by month enables partition pruning for time-bounded analytics (the common
-- access pattern here) and keeps per-partition indexes small.
--
-- Note: a partitioned table requires the partition key (date) to be part of every
-- unique/primary key, so the primary key becomes (id, date) instead of (id).
-- The table name and columns are otherwise unchanged, so existing API/queries work.
--
-- demo.sales_summary (migration 000009) is a VIEW on demo.sales. It must be
-- dropped before the table is swapped and recreated afterwards, otherwise the
-- legacy-table cleanup below would fail on the dependency.

DROP VIEW IF EXISTS demo.sales_summary;

-- Preserve any existing demo rows by parking the current table aside.
ALTER TABLE IF EXISTS demo.sales RENAME TO sales_legacy;

CREATE TABLE demo.sales (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    date DATE NOT NULL,
    product_category TEXT NOT NULL,
    product_name TEXT NOT NULL,
    quantity INT NOT NULL,
    unit_price DECIMAL(10,2) NOT NULL,
    total_amount DECIMAL(10,2) NOT NULL,
    region TEXT NOT NULL,
    sales_rep TEXT NOT NULL,
    PRIMARY KEY (id, date)
) PARTITION BY RANGE (date);

-- Monthly partitions across a generous window: 36 months back to 12 months ahead.
DO $$
DECLARE
    start_month DATE := date_trunc('month', CURRENT_DATE)::date - INTERVAL '36 months';
    m INT;
    p_start DATE;
    p_end DATE;
    p_name TEXT;
BEGIN
    FOR m IN 0..48 LOOP
        p_start := (start_month + (m || ' months')::interval)::date;
        p_end := (p_start + INTERVAL '1 month')::date;
        p_name := format('sales_%s', to_char(p_start, 'YYYY_MM'));
        EXECUTE format(
            'CREATE TABLE IF NOT EXISTS demo.%I PARTITION OF demo.sales FOR VALUES FROM (%L) TO (%L)',
            p_name, p_start, p_end
        );
    END LOOP;
END $$;

-- DEFAULT partition: a safety net so inserts outside the window never fail.
-- Tradeoff: rows landing here are not pruned efficiently; see docs/DATASET.md.
CREATE TABLE IF NOT EXISTS demo.sales_default PARTITION OF demo.sales DEFAULT;

-- Move preserved rows into the partitioned table (no-op if there were none).
DO $$
BEGIN
    IF to_regclass('demo.sales_legacy') IS NOT NULL THEN
        INSERT INTO demo.sales (id, date, product_category, product_name, quantity, unit_price, total_amount, region, sales_rep)
        SELECT id, date, product_category, product_name, quantity, unit_price, total_amount, region, sales_rep
        FROM demo.sales_legacy;
        DROP TABLE demo.sales_legacy;
    END IF;
END $$;

-- Indexes are declared on the parent and propagate to every partition (current and future).
CREATE INDEX IF NOT EXISTS idx_sales_date ON demo.sales(date);
CREATE INDEX IF NOT EXISTS idx_sales_category ON demo.sales(product_category);
CREATE INDEX IF NOT EXISTS idx_sales_region ON demo.sales(region);

-- Read-only role must be able to SELECT for user-run queries.
GRANT SELECT ON demo.sales TO pgquerynarrative_readonly;
ALTER DEFAULT PRIVILEGES IN SCHEMA demo GRANT SELECT ON TABLES TO pgquerynarrative_readonly;

-- Recreate the summary view (originally from migration 000009) on the new table.
CREATE OR REPLACE VIEW demo.sales_summary AS
SELECT
    product_category,
    COUNT(*) AS transaction_count,
    SUM(quantity) AS total_quantity,
    SUM(total_amount) AS total_revenue
FROM demo.sales
GROUP BY product_category;

GRANT SELECT ON demo.sales_summary TO pgquerynarrative_readonly;
