DROP INDEX IF EXISTS app.idx_webhook_deliveries_lease;
DROP INDEX IF EXISTS app.idx_webhook_deliveries_outbox_claim;

ALTER TABLE app.webhook_deliveries DROP CONSTRAINT IF EXISTS webhook_deliveries_status_check;
UPDATE app.webhook_deliveries SET status = 'failed' WHERE status IN ('pending', 'delivering');
ALTER TABLE app.webhook_deliveries ADD CONSTRAINT webhook_deliveries_status_check
    CHECK (status IN ('pending', 'delivered', 'failed', 'dead_letter'));

ALTER TABLE app.webhook_deliveries
    DROP COLUMN IF EXISTS response_class,
    DROP COLUMN IF EXISTS lease_until,
    DROP COLUMN IF EXISTS lease_owner,
    DROP COLUMN IF EXISTS next_attempt_at;

DROP INDEX IF EXISTS app.idx_reports_schedule_run_unique;
ALTER TABLE app.reports DROP COLUMN IF EXISTS schedule_run_id;
