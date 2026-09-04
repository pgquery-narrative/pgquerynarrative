DROP TRIGGER IF EXISTS trg_regression_alert_investigation_same_org ON app.regression_alerts;
DROP FUNCTION IF EXISTS app.regression_alert_investigation_same_org();

ALTER TABLE app.investigation_candidates
    DROP CONSTRAINT IF EXISTS investigation_candidates_inv_org_fkey;
ALTER TABLE app.investigation_candidates
    ADD CONSTRAINT investigation_candidates_investigation_id_fkey
    FOREIGN KEY (investigation_id) REFERENCES app.investigations (id) ON DELETE CASCADE;

ALTER TABLE app.investigations
    DROP CONSTRAINT IF EXISTS investigations_id_org_uniq;
