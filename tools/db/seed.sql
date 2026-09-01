BEGIN;

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
FROM generate_series(1, 8000);

COMMIT;
