-- Reports remember which schedule_run produced them (idempotent report generation across
-- worker retries/crash-recovery), and webhook_deliveries becomes a true transactional outbox
-- with lease-based claiming instead of a simple pending/delivered/failed audit log.

ALTER TABLE app.reports
    ADD COLUMN IF NOT EXISTS schedule_run_id UUID REFERENCES app.schedule_runs(id) ON DELETE SET NULL;

-- At most one report per schedule_run: recovery must reuse the existing report instead of
-- generating a second one (and therefore a second report_id) for the same run.
CREATE UNIQUE INDEX IF NOT EXISTS idx_reports_schedule_run_unique
    ON app.reports(schedule_run_id)
    WHERE schedule_run_id IS NOT NULL;

ALTER TABLE app.webhook_deliveries
    ADD COLUMN IF NOT EXISTS next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ADD COLUMN IF NOT EXISTS lease_owner TEXT,
    ADD COLUMN IF NOT EXISTS lease_until TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS response_class TEXT;

COMMENT ON COLUMN app.webhook_deliveries.lease_owner IS 'Worker id currently attempting delivery (outbox claim lease); paired with lease_until.';
COMMENT ON COLUMN app.webhook_deliveries.lease_until IS 'Lease expiry for status=delivering; expired leases are reclaimed to pending (crash recovery).';
COMMENT ON COLUMN app.webhook_deliveries.next_attempt_at IS 'Earliest time a pending delivery is eligible to be claimed again (exponential backoff).';
COMMENT ON COLUMN app.webhook_deliveries.response_class IS 'Coarse classification of the last delivery attempt (2xx, retryable_4xx, 4xx, 5xx, network_error).';

-- Fold the legacy terminal 'failed' status into the outbox model: still-retryable rows
-- become 'pending' (eligible immediately), exhausted ones become 'dead_letter'.
UPDATE app.webhook_deliveries
SET status = CASE WHEN attempt_count >= 5 THEN 'dead_letter' ELSE 'pending' END,
    next_attempt_at = NOW()
WHERE status = 'failed';

ALTER TABLE app.webhook_deliveries DROP CONSTRAINT IF EXISTS webhook_deliveries_status_check;
ALTER TABLE app.webhook_deliveries ADD CONSTRAINT webhook_deliveries_status_check
    CHECK (status IN ('pending', 'delivering', 'delivered', 'dead_letter'));

CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_outbox_claim
    ON app.webhook_deliveries(next_attempt_at)
    WHERE status = 'pending';

CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_lease
    ON app.webhook_deliveries(lease_until)
    WHERE status = 'delivering';
