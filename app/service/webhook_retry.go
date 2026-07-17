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
	maxWebhookAttempts  = 5
	webhookRetryBackoff = 2 * time.Minute
	maxScheduleAttempts = 5
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

	rows, err := tx.Query(ctx, `
		SELECT id, organization_id::text, schedule_id::text, destination_url, payload, attempt_count, idempotency_key
		FROM app.webhook_deliveries
		WHERE status = 'failed'
		  AND attempt_count < $1
		  AND (completed_at IS NULL OR completed_at <= NOW() - ($2::text || ' seconds')::interval)
		ORDER BY completed_at NULLS FIRST
		FOR UPDATE SKIP LOCKED
		LIMIT 20
	`, maxWebhookAttempts, int(webhookRetryBackoff.Seconds()))
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
		status := "delivered"
		errMsg := ""
		httpStatus := 0
		respBytes := 0
		if postErr != nil {
			status = "failed"
			errMsg = postErr.Error()
		} else {
			httpStatus = result.StatusCode
			respBytes = result.ResponseBytes
			if result.StatusCode >= 300 {
				status = "failed"
				errMsg = "webhook delivery failed"
			}
		}
		nextAttempts := item.attempts + 1
		if status == "failed" && nextAttempts >= maxWebhookAttempts {
			status = "dead_letter"
		}
		runCtx := auth.WithPrincipal(ctx, auth.Principal{UserID: "system", OrgID: item.orgID, Role: auth.RoleAdmin})
		_, _ = s.appPool.Exec(runCtx, `
			UPDATE app.webhook_deliveries
			SET status = $2,
			    attempt_count = $3,
			    http_status = $4,
			    response_bytes = $5,
			    error_message = NULLIF($6, ''),
			    completed_at = NOW()
			WHERE id = $1
		`, item.id, status, nextAttempts, nullInt(httpStatus), respBytes, errMsg)
	}
	return nil
}

// RecoverExpiredScheduleLeases reclaims stuck running schedule_runs whose lease expired.
func (s *SchedulesService) RecoverExpiredScheduleLeases(ctx context.Context, rawPool *pgxpool.Pool, workerID string) error {
	if rawPool == nil {
		rawPool = s.rawPool
	}
	if rawPool == nil {
		return errors.New("raw pool required for lease recovery")
	}
	tx, err := db.BeginSchedulerTx(ctx, rawPool)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `
		SELECT id, schedule_id, organization_id::text, attempt_count
		FROM app.schedule_runs
		WHERE status = 'running'
		  AND lease_until IS NOT NULL
		  AND lease_until < NOW()
		ORDER BY lease_until
		FOR UPDATE SKIP LOCKED
		LIMIT 20
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	type stuck struct {
		runID, scheduleID, orgID string
		attempts                 int
	}
	var items []stuck
	for rows.Next() {
		var s stuck
		if err := rows.Scan(&s.runID, &s.scheduleID, &s.orgID, &s.attempts); err != nil {
			return err
		}
		items = append(items, s)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, item := range items {
		next := item.attempts + 1
		if next >= maxScheduleAttempts {
			_, err := tx.Exec(ctx, `
				UPDATE app.schedule_runs
				SET status = 'dead_letter', failure_code = 'max_attempts',
				    failure_message = 'lease expired and max attempts reached',
				    lease_until = NULL, completed_at = NOW(), attempt_count = $2
				WHERE id = $1
			`, item.runID, next)
			if err != nil {
				return err
			}
			_, _ = tx.Exec(ctx, `
				UPDATE app.schedules SET locked_by = NULL, locked_until = NULL, updated_at = NOW() WHERE id = $1
			`, item.scheduleID)
			continue
		}
		leaseUntil := time.Now().UTC().Add(defaultScheduleLease)
		_, err := tx.Exec(ctx, `
			UPDATE app.schedule_runs
			SET attempt_count = $2, worker_id = $3, lease_until = $4, started_at = NOW(),
			    failure_code = 'lease_recovered', failure_message = 'previous worker lease expired'
			WHERE id = $1
		`, item.runID, next, workerID, leaseUntil)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			UPDATE app.schedules SET locked_by = $2, locked_until = $3, updated_at = NOW() WHERE id = $1
		`, item.scheduleID, workerID, leaseUntil)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
