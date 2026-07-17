package service

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"time"

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

// StartWebhookRetryWorker polls failed webhook deliveries and retries with backoff.
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
					log.Printf("webhook retry worker: %v", err)
				}
			}
		}
	}()
}

// RetryFailedWebhooks re-delivers failed webhook rows that are eligible for retry.
func (s *SchedulesService) RetryFailedWebhooks(ctx context.Context, rawPool *pgxpool.Pool) error {
	if rawPool == nil {
		rawPool = s.rawPool
	}
	if rawPool == nil {
		return errors.New("raw pool required for webhook retries")
	}
	tx, err := db.BeginSchedulerTx(ctx, rawPool)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	workerID := scheduleWorkerID()
	claimUntil := time.Now().UTC().Add(webhookClaimLease)
	rows, err := tx.Query(ctx, `
		UPDATE app.webhook_deliveries
		SET claimed_by = $1,
		    claimed_until = $2
		WHERE id IN (
			SELECT id
			FROM app.webhook_deliveries
			WHERE status = 'failed'
			  AND attempt_count < $3
			  AND (claimed_until IS NULL OR claimed_until < NOW())
			  AND (completed_at IS NULL OR completed_at <= NOW() - make_interval(secs => LEAST($4::int, (POWER(2, GREATEST(attempt_count, 0)) * $5)::int)))
			ORDER BY completed_at NULLS FIRST
			FOR UPDATE SKIP LOCKED
			LIMIT 20
		)
		RETURNING id, organization_id::text, schedule_id::text, destination_url, payload, attempt_count, idempotency_key
	`, workerID, claimUntil, maxWebhookAttempts, int(webhookRetryMaxBackoff.Seconds()), int(webhookRetryBaseBackoff.Seconds()))
	if err != nil {
		return err
	}
	defer rows.Close()

	type pending struct {
		id, orgID, scheduleID, url, idempotencyKey string
		payload                                    []byte
		attempts                                   int
	}
	var items []pending
	for rows.Next() {
		var p pending
		var scheduleID *string
		if err := rows.Scan(&p.id, &p.orgID, &scheduleID, &p.url, &p.payload, &p.attempts, &p.idempotencyKey); err != nil {
			return err
		}
		if scheduleID != nil {
			p.scheduleID = *scheduleID
		}
		items = append(items, p)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}

	client := s.webhookClient
	if client == nil {
		client = security.NewWebhookClient(s.webhookSecret, 10*time.Second, s.allowedHosts...)
	}
	for _, item := range items {
		var body map[string]any
		_ = json.Unmarshal(item.payload, &body)
		if body == nil {
			body = map[string]any{}
		}
		observability.IncWebhookDelivery()
		result, postErr := client.PostJSON(ctx, item.url, item.idempotencyKey, body)
		var status, errMsg string
		httpStatus := 0
		respBytes := 0
		if postErr != nil {
			status = "failed"
			errMsg = postErr.Error()
		} else {
			httpStatus = result.StatusCode
			respBytes = result.ResponseBytes
			status, errMsg = classifyWebhookHTTPStatus(result.StatusCode)
		}
		nextAttempts := item.attempts + 1
		if status == "failed" && nextAttempts >= maxWebhookAttempts {
			status = "dead_letter"
		}
		if status == "dead_letter" {
			observability.IncWebhookDeadLetter()
		}
		if status == "failed" || status == "dead_letter" {
			observability.IncWebhookFailure()
		}
		runCtx := auth.WithPrincipal(ctx, auth.Principal{UserID: "system", OrgID: item.orgID, Role: auth.RoleAdmin})
		_, _ = s.appPool.Exec(runCtx, `
			UPDATE app.webhook_deliveries
			SET status = $2,
			    attempt_count = $3,
			    http_status = $4,
			    response_bytes = $5,
			    error_message = NULLIF($6, ''),
			    completed_at = NOW(),
			    claimed_by = NULL,
			    claimed_until = NULL
			WHERE id = $1
		`, item.id, status, nextAttempts, nullInt(httpStatus), respBytes, errMsg)
	}
	return nil
}

func classifyWebhookHTTPStatus(code int) (status, errMsg string) {
	if code >= 200 && code < 300 {
		return "delivered", ""
	}
	// Permanent client errors: do not retry (except 408/429).
	if code >= 400 && code < 500 && code != 408 && code != 429 {
		return "dead_letter", "webhook permanent client error"
	}
	return "failed", "webhook delivery failed"
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
