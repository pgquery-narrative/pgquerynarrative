-- Revert demo.sales to a plain (non-partitioned) table with the original primary key.
--
-- demo.sales_summary (migration 000009) is a VIEW on demo.sales; drop it first,
-- then recreate it on the restored table at the end.

DROP VIEW IF EXISTS demo.sales_summary;

ALTER TABLE IF EXISTS demo.sales RENAME TO sales_partitioned;

CREATE TABLE demo.sales (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    date DATE NOT NULL,
    product_category TEXT NOT NULL,
    product_name TEXT NOT NULL,
    quantity INT NOT NULL,
    unit_price DECIMAL(10,2) NOT NULL,
    total_amount DECIMAL(10,2) NOT NULL,
    region TEXT NOT NULL,
    sales_rep TEXT NOT NULL
);

DO $$
BEGIN
    IF to_regclass('demo.sales_partitioned') IS NOT NULL THEN
        INSERT INTO demo.sales (id, date, product_category, product_name, quantity, unit_price, total_amount, region, sales_rep)
        SELECT id, date, product_category, product_name, quantity, unit_price, total_amount, region, sales_rep
        FROM demo.sales_partitioned;
        DROP TABLE demo.sales_partitioned CASCADE;
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_sales_date ON demo.sales(date);
CREATE INDEX IF NOT EXISTS idx_sales_category ON demo.sales(product_category);
CREATE INDEX IF NOT EXISTS idx_sales_region ON demo.sales(region);

GRANT SELECT ON demo.sales TO pgquerynarrative_readonly;
ALTER DEFAULT PRIVILEGES IN SCHEMA demo GRANT SELECT ON TABLES TO pgquerynarrative_readonly;

-- Recreate the summary view (originally from migration 000009).
CREATE OR REPLACE VIEW demo.sales_summary AS
SELECT
    product_category,
    COUNT(*) AS transaction_count,
    SUM(quantity) AS total_quantity,
    SUM(total_amount) AS total_revenue
FROM demo.sales
GROUP BY product_category;

GRANT SELECT ON demo.sales_summary TO pgquerynarrative_readonly;
