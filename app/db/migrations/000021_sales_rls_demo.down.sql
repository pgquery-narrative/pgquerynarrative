DROP POLICY IF EXISTS sales_select_own_rep ON demo.sales;
DROP POLICY IF EXISTS sales_select_api_readonly ON demo.sales;

ALTER TABLE demo.sales DISABLE ROW LEVEL SECURITY;

REVOKE SELECT ON demo.sales FROM pgquerynarrative_sales_rep;
REVOKE USAGE ON SCHEMA demo FROM pgquerynarrative_sales_rep;

DO $$
BEGIN
    IF COALESCE((SELECT rolsuper FROM pg_roles WHERE rolname = current_user), false) THEN
        ALTER ROLE pgquerynarrative_sales_rep RESET default_transaction_read_only;
    END IF;
END
$$;

DROP ROLE IF EXISTS pgquerynarrative_sales_rep;
