-- B10: Row-level security demo on demo.sales — multi-tenant visibility by sales_rep.
--
-- API user SQL (pgquerynarrative_readonly) keeps full SELECT via a permissive policy.
-- Direct sessions as pgquerynarrative_sales_rep see only rows where
-- sales_rep = current_setting('app.current_rep', true) after SET app.current_rep = '…'.
-- Table owner (pgquerynarrative_app) bypasses RLS for migrations and seeding.

DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = 'pgquerynarrative_sales_rep') THEN
        CREATE ROLE pgquerynarrative_sales_rep LOGIN PASSWORD 'pgquerynarrative_sales_rep';
    END IF;
END
$$;

GRANT USAGE ON SCHEMA demo TO pgquerynarrative_sales_rep;
GRANT SELECT ON demo.sales TO pgquerynarrative_sales_rep;

-- Read-only at session level (same pattern as pgquerynarrative_readonly, migration 000011).
DO $$
BEGIN
    IF COALESCE((SELECT rolsuper FROM pg_roles WHERE rolname = current_user), false) THEN
        ALTER ROLE pgquerynarrative_sales_rep SET default_transaction_read_only = on;
    END IF;
END
$$;

ALTER TABLE demo.sales ENABLE ROW LEVEL SECURITY;

-- User-facing API / EXPLAIN / pg_stat_statements path: unchanged visibility.
CREATE POLICY sales_select_api_readonly ON demo.sales
    FOR SELECT
    TO pgquerynarrative_readonly
    USING (true);

-- Multi-tenant demo: one rep per session via custom GUC.
CREATE POLICY sales_select_own_rep ON demo.sales
    FOR SELECT
    TO pgquerynarrative_sales_rep
    USING (sales_rep = current_setting('app.current_rep', true));
