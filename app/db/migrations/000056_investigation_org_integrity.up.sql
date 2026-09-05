-- Cross-organization integrity for investigation children.
--
-- app.investigation_candidates and app.regression_alerts each carry their own
-- organization_id plus an investigation_id, with nothing forcing the two to
-- agree. Enforce the match at the database level.

-- Composite-FK target: an investigation is uniquely identified by (id, org).
ALTER TABLE app.investigations
    ADD CONSTRAINT investigations_id_org_uniq UNIQUE (id, organization_id);

-- investigation_candidates: replace the single-column FK with a composite one so
-- a candidate's organization_id must equal its investigation's.
DELETE FROM app.investigation_candidates c
 USING app.investigations i
 WHERE c.investigation_id = i.id
   AND c.organization_id <> i.organization_id;

ALTER TABLE app.investigation_candidates
    DROP CONSTRAINT IF EXISTS investigation_candidates_investigation_id_fkey;
ALTER TABLE app.investigation_candidates
    ADD CONSTRAINT investigation_candidates_inv_org_fkey
    FOREIGN KEY (investigation_id, organization_id)
    REFERENCES app.investigations (id, organization_id) ON DELETE CASCADE;

-- regression_alerts.investigation_id is nullable while organization_id is NOT
-- NULL, so a composite FK with ON DELETE SET NULL cannot null both columns. Keep
-- the single-column FK (ON DELETE SET NULL) and enforce the org match with a
-- trigger.
UPDATE app.regression_alerts ra
   SET investigation_id = NULL
  FROM app.investigations i
 WHERE ra.investigation_id = i.id
   AND ra.organization_id <> i.organization_id;

CREATE OR REPLACE FUNCTION app.regression_alert_investigation_same_org()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.investigation_id IS NOT NULL THEN
        PERFORM 1 FROM app.investigations i
         WHERE i.id = NEW.investigation_id
           AND i.organization_id = NEW.organization_id;
        IF NOT FOUND THEN
            RAISE EXCEPTION 'regression_alerts.investigation_id % is not in organization %',
                NEW.investigation_id, NEW.organization_id;
        END IF;
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_regression_alert_investigation_same_org ON app.regression_alerts;
CREATE TRIGGER trg_regression_alert_investigation_same_org
    BEFORE INSERT OR UPDATE OF investigation_id, organization_id ON app.regression_alerts
    FOR EACH ROW EXECUTE FUNCTION app.regression_alert_investigation_same_org();
