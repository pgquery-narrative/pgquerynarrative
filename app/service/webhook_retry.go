package service

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/app/db"
	"github.com/pgquerynarrative/pgquerynarrative/app/observability"
	"github.com/pgquerynarrative/pgquerynarrative/app/security"
)

const (
	maxWebhookAttempts      = 5
	webhookRetryBaseBackoff = 30 * time.Second
	webhookRetryMaxBackoff  = 30 * time.Minute
	webhookClaimLease       = 5 * time.Minute
	maxScheduleAttempts     = 5
)

// StartWebhookRetryWorker polls the webhook outbox and delivers pending rows with backoff.
// This is the durable half of the outbox pattern: deliverReport enqueues a row before any
// network I/O and makes one best-effort inline attempt, and this worker retries anything left
// pending — including rows whose "delivering" lease expired because a worker crashed
// mid-attempt (the row and its stable delivery ID already exist, so recovery retries the same
// delivery instead of fabricating a new one).
func StartWebhookRetryWorker(ctx context.Context, rawPool *pgxpool.Pool, svc *SchedulesService, interval time.Duration) {
	if rawPool == nil || svc == nil || interval <= 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := svc.RetryFailedWebhooks(ctx, rawPool); err != nil {
					log.Printf("webhook outbox worker: %v", err)
				}
			}
		}
	}()
}

// RetryFailedWebhooks reclaims outbox rows abandoned by a crashed worker, then atomically
// claims and delivers due pending rows across all organizations. It is the single code path
// for both first-attempt recovery (the inline attempt never ran, e.g. process crash right
// after enqueue) and backoff retries, so every attempt reuses the same delivery ID.
func (s *SchedulesService) RetryFailedWebhooks(ctx context.Context, rawPool *pgxpool.Pool) error {
	if rawPool == nil {
		rawPool = s.rawPool
	}
	if rawPool == nil {
		return errors.New("raw pool required for webhook outbox processing")
	}
	if err := reclaimStuckOutboxLeases(ctx, rawPool); err != nil {
		log.Printf("webhook outbox lease reclaim: %v", err)
	}

	tx, err := db.BeginSchedulerTx(ctx, rawPool)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	workerID := scheduleWorkerID()
	leaseUntil := time.Now().UTC().Add(webhookClaimLease)
	rows, err := tx.Query(ctx, `
		UPDATE app.webhook_deliveries
		SET status = 'delivering',
		    lease_owner = $1,
		    lease_until = $2,
		    attempt_count = attempt_count + 1
		WHERE id IN (
			SELECT id
			FROM app.webhook_deliveries
			WHERE status = 'pending'
			  AND next_attempt_at <= NOW()
			ORDER BY next_attempt_at
			FOR UPDATE SKIP LOCKED
			LIMIT 20
		)
		RETURNING id, organization_id::text, schedule_run_id::text, destination_url, payload, attempt_count, idempotency_key
	`, workerID, leaseUntil)
	if err != nil {
		return err
	}
	defer rows.Close()

	type claimed struct {
		id, orgID, scheduleRunID, url, idempotencyKey string
		payload                                       []byte
		attempts                                      int
	}
	var items []claimed
	for rows.Next() {
		var c claimed
		var scheduleRunID *string
		if err := rows.Scan(&c.id, &c.orgID, &scheduleRunID, &c.url, &c.payload, &c.attempts, &c.idempotencyKey); err != nil {
			return err
		}
		if scheduleRunID != nil {
			c.scheduleRunID = *scheduleRunID
		}
		items = append(items, c)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}

	for _, item := range items {
		status, httpStatus, respBytes, errMsg, responseClass := s.attemptOutboxHTTP(ctx, item.url, item.idempotencyKey, item.payload)
		final, nextAttemptAt := classifyOutboxResult(status, item.attempts)
		if final == "dead_letter" {
			observability.IncWebhookDeadLetter()
		}
		if final == "dead_letter" || status == "failed" {
			observability.IncWebhookFailure()
		}
		runCtx := auth.WithPrincipal(ctx, auth.Principal{UserID: "system", OrgID: item.orgID, Role: auth.RoleTenantAdmin})
		_, _ = s.appPool.Exec(runCtx, `
			UPDATE app.webhook_deliveries
			SET status = $2,
			    http_status = $3,
			    response_bytes = $4,
			    error_message = NULLIF($5, ''),
			    response_class = NULLIF($6, ''),
			    next_attempt_at = $7,
			    completed_at = CASE WHEN $2 IN ('delivered', 'dead_letter') THEN NOW() ELSE completed_at END,
			    lease_owner = NULL,
			    lease_until = NULL
			WHERE id = $1
		`, item.id, final, nullInt(httpStatus), respBytes, errMsg, responseClass, nextAttemptAt)
		if item.scheduleRunID != "" && (final == "delivered" || final == "dead_letter") {
			s.finalizeScheduleRunAfterDelivery(runCtx, item.scheduleRunID, final, errMsg)
		}
	}
	return nil
}

// reclaimStuckOutboxLeases returns delivery rows abandoned by a crashed/killed worker
// (status='delivering' with an expired lease) to 'pending' so they can be claimed again. The
// row — and therefore its stable idempotency_key / delivery ID — already exists, so recovery
// retries the same delivery instead of regenerating a report and sending a fresh ID.
func reclaimStuckOutboxLeases(ctx context.Context, rawPool *pgxpool.Pool) error {
	tx, err := db.BeginSchedulerTx(ctx, rawPool)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		UPDATE app.webhook_deliveries
		SET status = 'pending', lease_owner = NULL, lease_until = NULL, next_attempt_at = NOW()
		WHERE status = 'delivering' AND lease_until IS NOT NULL AND lease_until < NOW()
	`); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// attemptOutboxDelivery atomically claims a single pending, due outbox row and makes one
// delivery attempt inline. Used right after enqueueing so the common case delivers with low
// latency instead of waiting for the next background sweep. If the row is not currently
// eligible (already delivering elsewhere, or already terminal), it returns the row's current
// status without attempting delivery again.
func (s *SchedulesService) attemptOutboxDelivery(ctx context.Context, deliveryID string) (string, error) {
	workerID := scheduleWorkerID()
	leaseUntil := time.Now().UTC().Add(webhookClaimLease)
	var destinationURL, idempotencyKey string
	var payload []byte
	var attemptCount int
	var scheduleRunID *string
	err := s.appPool.QueryRow(ctx, `
		UPDATE app.webhook_deliveries
		SET status = 'delivering', lease_owner = $2, lease_until = $3, attempt_count = attempt_count + 1
		WHERE id = $1 AND status = 'pending' AND next_attempt_at <= NOW()
		RETURNING destination_url, idempotency_key, payload, attempt_count, schedule_run_id::text
	`, deliveryID, workerID, leaseUntil).Scan(&destinationURL, &idempotencyKey, &payload, &attemptCount, &scheduleRunID)
	if errors.Is(err, pgx.ErrNoRows) {
		return s.outboxStatus(ctx, deliveryID)
	}
	if err != nil {
		return "pending", err
	}

	status, httpStatus, respBytes, errMsg, responseClass := s.attemptOutboxHTTP(ctx, destinationURL, idempotencyKey, payload)
	final, nextAttemptAt := classifyOutboxResult(status, attemptCount)
	if final == "dead_letter" {
		observability.IncWebhookDeadLetter()
	}
	if final == "dead_letter" || status == "failed" {
		observability.IncWebhookFailure()
	}
	_, updErr := s.appPool.Exec(ctx, `
		UPDATE app.webhook_deliveries
		SET status = $2,
		    http_status = $3,
		    response_bytes = $4,
		    error_message = NULLIF($5, ''),
		    response_class = NULLIF($6, ''),
		    next_attempt_at = $7,
		    completed_at = CASE WHEN $2 IN ('delivered', 'dead_letter') THEN NOW() ELSE completed_at END,
		    lease_owner = NULL,
		    lease_until = NULL
		WHERE id = $1
	`, deliveryID, final, nullInt(httpStatus), respBytes, errMsg, responseClass, nextAttemptAt)
	if updErr != nil {
		return final, updErr
	}
	if scheduleRunID != nil && *scheduleRunID != "" && (final == "delivered" || final == "dead_letter") {
		s.finalizeScheduleRunAfterDelivery(ctx, *scheduleRunID, final, errMsg)
	}
	return final, nil
}

func (s *SchedulesService) outboxStatus(ctx context.Context, deliveryID string) (string, error) {
	var status string
	if err := s.appPool.QueryRow(ctx, `SELECT status FROM app.webhook_deliveries WHERE id = $1`, deliveryID).Scan(&status); err != nil {
		return "pending", err
	}
	return status, nil
}

// classifyOutboxResult applies max-attempt dead-lettering and exponential backoff to a raw
// delivery outcome. status is "delivered", "dead_letter", or "failed" (meaning retryable).
func classifyOutboxResult(status string, attemptCount int) (final string, nextAttemptAt time.Time) {
	now := time.Now().UTC()
	switch status {
	case "delivered", "dead_letter":
		return status, now
	default: // "failed": retry unless attempts are exhausted
		if attemptCount >= maxWebhookAttempts {
			return "dead_letter", now
		}
		return "pending", now.Add(webhookBackoff(attemptCount))
	}
}

func webhookBackoff(attemptCount int) time.Duration {
	if attemptCount < 0 {
		attemptCount = 0
	}
	// Double toward the cap rather than computing base * 2^attemptCount: that
	// product overflows int64 somewhere past attempt 29 and wraps negative, which
	// would schedule the next attempt in the past and spin the outbox. Bailing out
	// once the next double would pass the cap keeps every value in range.
	d := webhookRetryBaseBackoff
	for i := 0; i < attemptCount; i++ {
		if d >= webhookRetryMaxBackoff/2 {
			return webhookRetryMaxBackoff
		}
		d *= 2
	}
	if d > webhookRetryMaxBackoff {
		return webhookRetryMaxBackoff
	}
	return d
}

// attemptOutboxHTTP performs one signed webhook POST and classifies the raw HTTP outcome.
// deliveryID is reused verbatim across every attempt (X-PGQN-Delivery-ID header) so retries —
// and receiver-side dedup — stay stable even across process restarts.
func (s *SchedulesService) attemptOutboxHTTP(ctx context.Context, destinationURL, deliveryID string, payload []byte) (status string, httpStatus, respBytes int, errMsg, responseClass string) {
	client := s.webhookClient
	if client == nil {
		client = security.NewWebhookClient(s.webhookSecret, 10*time.Second, s.allowedHosts...)
	}
	var body map[string]any
	_ = json.Unmarshal(payload, &body)
	if body == nil {
		body = map[string]any{}
	}
	observability.IncWebhookDelivery()
	result, err := client.PostJSON(ctx, destinationURL, deliveryID, body)
	if err != nil {
		return "failed", 0, 0, err.Error(), "network_error"
	}
	return classifyDeliveryHTTPStatus(result.StatusCode, result.ResponseBytes)
}

// classifyDeliveryHTTPStatus is the pure classification rule shared by every delivery
// attempt: 2xx succeeds, 408/429 retry, other 4xx dead-letter (permanent client error), and
// everything else (5xx, unexpected codes) retries.
func classifyDeliveryHTTPStatus(code, respBytesIn int) (status string, httpStatus, respBytes int, errMsg, responseClass string) {
	switch {
	case code >= 200 && code < 300:
		return "delivered", code, respBytesIn, "", "2xx"
	case code == 408 || code == 429:
		return "failed", code, respBytesIn, "webhook delivery failed", "retryable_4xx"
	case code >= 400 && code < 500:
		return "dead_letter", code, respBytesIn, "webhook permanent client error", "4xx"
	default:
		return "failed", code, respBytesIn, "webhook delivery failed", "5xx"
	}
}

// RecoverExpiredScheduleLeases reclaims stuck running schedule_runs whose lease expired.
// It returns claimed runs that this worker should execute.
func (s *SchedulesService) RecoverExpiredScheduleLeases(ctx context.Context, rawPool *pgxpool.Pool, workerID string) ([]claimedScheduleRun, error) {
	if rawPool == nil {
		rawPool = s.rawPool
	}
	if rawPool == nil {
		return nil, errors.New("raw pool required for lease recovery")
	}
	tx, err := db.BeginSchedulerTx(ctx, rawPool)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `
		SELECT id, schedule_id, organization_id::text, attempt_count, scheduled_for
		FROM app.schedule_runs
		WHERE status = 'running'
		  AND lease_until IS NOT NULL
		  AND lease_until < NOW()
		ORDER BY lease_until
		FOR UPDATE SKIP LOCKED
		LIMIT 20
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type stuck struct {
		runID, scheduleID, orgID string
		attempts                 int
		scheduledFor             time.Time
	}
	var items []stuck
	for rows.Next() {
		var item stuck
		if err := rows.Scan(&item.runID, &item.scheduleID, &item.orgID, &item.attempts, &item.scheduledFor); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var recovered []claimedScheduleRun
	for _, item := range items {
		next := item.attempts + 1
		if next >= maxScheduleAttempts {
			observability.IncScheduleDeadLetter()
			_, err := tx.Exec(ctx, `
				UPDATE app.schedule_runs
				SET status = 'dead_letter', failure_code = 'max_attempts',
				    failure_message = 'lease expired and max attempts reached',
				    lease_until = NULL, completed_at = NOW(), attempt_count = $2
				WHERE id = $1
			`, item.runID, next)
			if err != nil {
				return nil, err
			}
			_, _ = tx.Exec(ctx, `
				UPDATE app.schedules SET locked_by = NULL, locked_until = NULL, updated_at = NOW() WHERE id = $1
			`, item.scheduleID)
			continue
		}
		observability.IncScheduleLeaseRecovery()
		leaseUntil := time.Now().UTC().Add(defaultScheduleLease)
		_, err := tx.Exec(ctx, `
			UPDATE app.schedule_runs
			SET attempt_count = $2, worker_id = $3, lease_until = $4, started_at = NOW(),
			    failure_code = 'lease_recovered', failure_message = 'previous worker lease expired'
			WHERE id = $1
		`, item.runID, next, workerID, leaseUntil)
		if err != nil {
			return nil, err
		}
		_, err = tx.Exec(ctx, `
			UPDATE app.schedules SET locked_by = $2, locked_until = $3, updated_at = NOW() WHERE id = $1
		`, item.scheduleID, workerID, leaseUntil)
		if err != nil {
			return nil, err
		}
		recovered = append(recovered, claimedScheduleRun{
			RunID:        item.runID,
			ScheduleID:   item.scheduleID,
			OrgID:        item.orgID,
			ScheduledFor: item.scheduledFor,
		})
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return recovered, nil
}
