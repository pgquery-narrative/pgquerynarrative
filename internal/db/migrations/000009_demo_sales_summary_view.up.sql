-- Read-only view: sales aggregated by product category.
-- Schema discovery (information_schema.columns) and validator already support views in allowed schemas.
CREATE OR REPLACE VIEW demo.sales_summary AS
SELECT
    product_category,
    COUNT(*) AS transaction_count,
    SUM(quantity) AS total_quantity,
    SUM(total_amount) AS total_revenue
FROM demo.sales
GROUP BY product_category;

GRANT SELECT ON demo.sales_summary TO pgquerynarrative_readonly;
