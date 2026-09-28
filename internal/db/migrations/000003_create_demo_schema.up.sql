CREATE TABLE IF NOT EXISTS demo.sales (
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

CREATE INDEX IF NOT EXISTS idx_sales_date ON demo.sales(date);
CREATE INDEX IF NOT EXISTS idx_sales_category ON demo.sales(product_category);
CREATE INDEX IF NOT EXISTS idx_sales_region ON demo.sales(region);

-- Read-only role must be able to SELECT for user-run queries
GRANT SELECT ON demo.sales TO pgquerynarrative_readonly;
ALTER DEFAULT PRIVILEGES IN SCHEMA demo GRANT SELECT ON TABLES TO pgquerynarrative_readonly;
