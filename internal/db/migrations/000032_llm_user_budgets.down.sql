DROP POLICY IF EXISTS llm_user_budget_usage_org ON app.llm_user_budget_usage;
ALTER TABLE app.llm_user_budget_usage DISABLE ROW LEVEL SECURITY;
DROP TABLE IF EXISTS app.llm_user_budget_usage;
