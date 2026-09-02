package service

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pgquerynarrative/pgquerynarrative/api/gen/schedules"
)

// ListRuns returns execution history for a schedule in the caller's organization.
func (s *SchedulesService) ListRuns(ctx context.Context, payload *schedules.ListRunsPayload) (*schedules.ScheduleRunListResult, error) {
	if _, err := s.getByID(ctx, payload.ID); err != nil {
		return nil, err
	}
	rows, err := s.appPool.Query(ctx, `
		SELECT id, schedule_id, status, attempt_count, scheduled_for, started_at, completed_at,
		       report_id, failure_code, failure_message
		FROM app.schedule_runs
		WHERE schedule_id = $1 AND organization_id = $2
		ORDER BY scheduled_for DESC
		LIMIT 100
	`, payload.ID, orgID(ctx))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]*schedules.ScheduleRunRecord, 0)
	for rows.Next() {
		item, err := scanScheduleRun(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return &schedules.ScheduleRunListResult{Items: items}, rows.Err()
}

// RetryRun requeues a failed or dead-letter schedule run via an atomic claim and the durable worker path.
func (s *SchedulesService) RetryRun(ctx context.Context, payload *schedules.RetryRunPayload) (*schedules.ScheduleRunRecord, error) {
	workerID := scheduleWorkerID()
	leaseUntil := time.Now().UTC().Add(defaultScheduleLease)
	row := s.appPool.QueryRow(ctx, `
		UPDATE app.schedule_runs
		SET status = 'running', attempt_count = attempt_count + 1,
		    lease_until = $2, worker_id = $4, started_at = NOW(), completed_at = NULL,
		    failure_code = NULL, failure_message = NULL
		WHERE id = $1 AND organization_id = $3
		  AND status IN ('failed', 'dead_letter')
		RETURNING id, schedule_id, status, attempt_count, scheduled_for, started_at, completed_at,
		          report_id, failure_code, failure_message
	`, payload.RunID, leaseUntil, orgID(ctx), workerID)
	current, err := scanScheduleRun(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, &schedules.NotFoundError{Name: "not_found", Message: "schedule run not found or not retryable", Code: strPtr("NOT_FOUND")}
		}
		return nil, err
	}
	_, _ = s.appPool.Exec(ctx, `
		UPDATE app.schedules
		SET locked_by = $2, locked_until = $3, updated_at = NOW()
		WHERE id = $1 AND organization_id = $4
	`, current.ScheduleID, workerID, leaseUntil, orgID(ctx))

	scheduledFor, _ := time.Parse(time.RFC3339, current.ScheduledFor)
	claim := claimedScheduleRun{
		RunID:        current.ID,
		ScheduleID:   current.ScheduleID,
		OrgID:        orgID(ctx),
		ScheduledFor: scheduledFor,
	}
	_ = s.executeClaimedRun(ctx, workerID, claim)

	row = s.appPool.QueryRow(ctx, `
		SELECT id, schedule_id, status, attempt_count, scheduled_for, started_at, completed_at,
		       report_id, failure_code, failure_message
		FROM app.schedule_runs
		WHERE id = $1 AND organization_id = $2
	`, payload.RunID, orgID(ctx))
	return scanScheduleRun(row)
}

// ListDeliveries returns recent webhook delivery attempts for the caller's organization.
func (s *SchedulesService) ListDeliveries(ctx context.Context) (*schedules.WebhookDeliveryListResult, error) {
	rows, err := s.appPool.Query(ctx, `
		SELECT id, schedule_id, destination_url, status, attempt_count, http_status, error_message, created_at, completed_at
		FROM app.webhook_deliveries
		WHERE organization_id = $1
		ORDER BY created_at DESC
		LIMIT 100
	`, orgID(ctx))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]*schedules.WebhookDeliveryRecord, 0)
	for rows.Next() {
		var rec schedules.WebhookDeliveryRecord
		var scheduleID *string
		var httpStatus *int
		var errMsg *string
		var completedAt *time.Time
		var createdAt time.Time
		if err := rows.Scan(&rec.ID, &scheduleID, &rec.DestinationURL, &rec.Status, &rec.AttemptCount, &httpStatus, &errMsg, &createdAt, &completedAt); err != nil {
			return nil, err
		}
		rec.CreatedAt = createdAt.UTC().Format(time.RFC3339)
		if scheduleID != nil {
			rec.ScheduleID = scheduleID
		}
		if httpStatus != nil {
			rec.HTTPStatus = httpStatus
		}
		if errMsg != nil {
			rec.ErrorMessage = errMsg
		}
		if completedAt != nil {
			v := completedAt.UTC().Format(time.RFC3339)
			rec.CompletedAt = &v
		}
		items = append(items, &rec)
	}
	return &schedules.WebhookDeliveryListResult{Items: items}, rows.Err()
}

func scanScheduleRun(r scanner) (*schedules.ScheduleRunRecord, error) {
	var rec schedules.ScheduleRunRecord
	var startedAt, completedAt *time.Time
	var reportID, failureCode, failureMsg *string
	var scheduledFor time.Time
	if err := r.Scan(&rec.ID, &rec.ScheduleID, &rec.Status, &rec.AttemptCount, &scheduledFor, &startedAt, &completedAt, &reportID, &failureCode, &failureMsg); err != nil {
		return nil, err
	}
	rec.ScheduledFor = scheduledFor.UTC().Format(time.RFC3339)
	if startedAt != nil {
		v := startedAt.UTC().Format(time.RFC3339)
		rec.StartedAt = &v
	}
	if completedAt != nil {
		v := completedAt.UTC().Format(time.RFC3339)
		rec.CompletedAt = &v
	}
	if reportID != nil {
		rec.ReportID = reportID
	}
	if failureCode != nil {
		rec.FailureCode = failureCode
	}
	if failureMsg != nil {
		rec.FailureMessage = failureMsg
	}
	return &rec, nil
}
