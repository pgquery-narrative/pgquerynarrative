DROP POLICY IF EXISTS llm_budget_reservations_org ON app.llm_budget_reservations;
ALTER TABLE app.llm_budget_reservations DISABLE ROW LEVEL SECURITY;
DROP TABLE IF EXISTS app.llm_budget_reservations;
