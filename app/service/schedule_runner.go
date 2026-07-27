package service

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/app/db"
	"github.com/pgquerynarrative/pgquerynarrative/app/observability"
)

const defaultScheduleLease = 5 * time.Minute

// StartScheduleRunner polls for due schedules and executes them with durable leases.
func StartScheduleRunner(ctx context.Context, rawPool *pgxpool.Pool, svc *SchedulesService, interval time.Duration) {
	if svc == nil || rawPool == nil || interval <= 0 {
		return
	}
	svc.SetRawPool(rawPool)
	workerID := scheduleWorkerID()
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				recovered, err := svc.RecoverExpiredScheduleLeases(ctx, rawPool, workerID)
				if err != nil {
					log.Printf("schedule lease recovery: %v", err)
				}
				for _, claim := range recovered {
					observability.IncSchedulerRun()
					if err := svc.executeClaimedRun(ctx, workerID, claim); err != nil {
						observability.IncSchedulerFailure()
						log.Printf("recovered schedule run %s failed: %v", claim.RunID, err)
					}
				}
				if err := svc.RunDue(ctx, workerID); err != nil {
					log.Printf("schedule runner: %v", err)
				}
			}
		}
	}()
}

func scheduleWorkerID() string {
	if host, err := os.Hostname(); err == nil && host != "" {
		return host
	}
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "worker-" + hex.EncodeToString(b)
}

type claimedScheduleRun struct {
	RunID        string
	ScheduleID   string
	OrgID        string
	ScheduledFor time.Time
}

// RunDue claims due schedules atomically and executes each claimed run once per replica.
func (s *SchedulesService) RunDue(ctx context.Context, workerID string) error {
	if workerID == "" {
		workerID = scheduleWorkerID()
	}
	claimed, err := s.claimDueSchedules(ctx, workerID)
	if err != nil {
		return err
	}
	for _, claim := range claimed {
		observability.IncSchedulerRun()
		if err := s.executeClaimedRun(ctx, workerID, claim); err != nil {
			observability.IncSchedulerFailure()
			log.Printf("schedule run %s failed: %v", claim.RunID, err)
		}
	}
	return nil
}

func (s *SchedulesService) claimDueSchedules(ctx context.Context, workerID string) ([]claimedScheduleRun, error) {
	raw := s.rawPool
	if raw == nil {
		return nil, errors.New("raw pool required for durable schedule claiming")
	}
	tx, err := db.BeginSchedulerTx(ctx, raw)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `
		SELECT s.id, COALESCE(s.organization_id, $1::uuid), s.next_run_at
		FROM app.schedules s
		WHERE s.enabled = true
		  AND s.next_run_at IS NOT NULL
		  AND s.next_run_at <= NOW()
		  AND (s.locked_until IS NULL OR s.locked_until < NOW())
		ORDER BY s.next_run_at
		FOR UPDATE SKIP LOCKED
		LIMIT 10
	`, auth.DefaultOrganizationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type dueSchedule struct {
		id           string
		orgID        string
		scheduledFor time.Time
	}
	var due []dueSchedule
	for rows.Next() {
		var item dueSchedule
		if err := rows.Scan(&item.id, &item.orgID, &item.scheduledFor); err != nil {
			return nil, err
		}
		due = append(due, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	leaseUntil := time.Now().UTC().Add(defaultScheduleLease)
	var claimed []claimedScheduleRun
	for _, item := range due {
		idempotencyKey := fmt.Sprintf("%s:%d", item.id, item.scheduledFor.UTC().Unix())
		var runID string
		err := tx.QueryRow(ctx, `
			INSERT INTO app.schedule_runs (
				schedule_id, organization_id, scheduled_for, idempotency_key,
				worker_id, lease_until, status, attempt_count, started_at
			) VALUES ($1,$2,$3,$4,$5,$6,'running',1,NOW())
			ON CONFLICT (idempotency_key) DO NOTHING
			RETURNING id
		`, item.id, item.orgID, item.scheduledFor, idempotencyKey, workerID, leaseUntil).Scan(&runID)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE app.schedules
			SET locked_by = $2, locked_until = $3, updated_at = NOW()
			WHERE id = $1
		`, item.id, workerID, leaseUntil); err != nil {
			return nil, err
		}
		claimed = append(claimed, claimedScheduleRun{
			RunID:        runID,
			ScheduleID:   item.id,
			OrgID:        item.orgID,
			ScheduledFor: item.scheduledFor,
		})
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return claimed, nil
}

func (s *SchedulesService) executeClaimedRun(ctx context.Context, workerID string, claim claimedScheduleRun) error {
	ownerUserID, ownerRole, err := s.resolveScheduleOwner(ctx, claim.ScheduleID, claim.OrgID)
	if err != nil {
		runCtx := auth.WithPrincipal(ctx, auth.Principal{UserID: workerID, OrgID: claim.OrgID, Role: auth.RoleTenantAdmin})
		_ = s.disableSchedule(runCtx, claim.ScheduleID, err.Error())
		return s.finishScheduleRun(runCtx, claim.RunID, "", "failed", err, "owner_unauthorized")
	}
	runCtx := auth.WithPrincipal(ctx, auth.Principal{UserID: ownerUserID, OrgID: claim.OrgID, Role: ownerRole})
	if s.reportsSvc == nil {
		return s.finishScheduleRun(runCtx, claim.RunID, "", "failed", errors.New("reports service not configured"), "misconfigured")
	}
	stopHeartbeat := s.startScheduleHeartbeat(runCtx, claim.RunID, claim.ScheduleID, workerID)
	defer stopHeartbeat()

	sc, err := s.getByIDForOrg(runCtx, claim.ScheduleID, claim.OrgID)
	if err != nil {
		return s.finishScheduleRun(runCtx, claim.RunID, "", "failed", err, "schedule_not_found")
	}
	if err := checkConnectionAccess(runCtx, s.authz, sc.ConnectionID, auth.ActionSchedule); err != nil {
		_ = s.disableSchedule(runCtx, claim.ScheduleID, err.Error())
		return s.finishScheduleRun(runCtx, claim.RunID, "", "failed", err, "owner_unauthorized")
	}
	reportID, deliveryStatus, runErr := s.runSchedule(runCtx, sc, claim.RunID)

	// Advance scheduling cadence regardless of webhook delivery outcome: cadence and outbox
	// delivery retries are independent concerns. last_status is informational text only.
	scheduleStatus := "completed"
	lastErr := ""
	switch {
	case runErr != nil:
		scheduleStatus = "failed"
		lastErr = SanitizeStoredError(runErr)
	case deliveryStatus == "dead_letter":
		scheduleStatus = "dead_letter"
		lastErr = "webhook delivery dead-lettered"
	case deliveryStatus == "pending":
		scheduleStatus = "pending_delivery"
	}
	nextRun, _ := computeNextRun(sc.IntervalExpr, time.Now().UTC())
	_, _ = s.appPool.Exec(runCtx, `
		UPDATE app.schedules
		SET last_run_at = NOW(), last_status = $2, last_error = NULLIF($3, ''),
		    next_run_at = $4, locked_by = NULL, locked_until = NULL, updated_at = NOW()
		WHERE id = $1
	`, sc.ID, scheduleStatus, lastErr, nextRun)

	if runErr != nil {
		return s.finishScheduleRun(runCtx, claim.RunID, reportID, "failed", runErr, "")
	}
	switch deliveryStatus {
	case "dead_letter":
		return s.finishScheduleRun(runCtx, claim.RunID, reportID, "dead_letter", errors.New("webhook delivery dead-lettered"), "webhook_dead_letter")
	case "pending":
		// Report generated and the webhook is durably enqueued; leave the schedule_run
		// "running" so the outbox worker (RetryFailedWebhooks) — or lease recovery, if this
		// worker dies — finalizes it once delivery reaches delivered/dead_letter.
		return nil
	default: // "completed" (log) or "delivered" (webhook resolved synchronously)
		return s.finishScheduleRun(runCtx, claim.RunID, reportID, "completed", nil, "")
	}
}

func (s *SchedulesService) resolveScheduleOwner(ctx context.Context, scheduleID, organizationID string) (userID, role string, err error) {
	var createdBy sql.NullString
	err = s.appPool.QueryRow(ctx, `
		SELECT created_by FROM app.schedules WHERE id = $1 AND organization_id = $2
	`, scheduleID, organizationID).Scan(&createdBy)
	if err != nil {
		return "", "", err
	}
	owner := strings.TrimSpace(createdBy.String)
	if owner == "" {
		return "", "", errors.New("schedule has no owner; re-create the schedule")
	}
	var memberRole string
	err = s.appPool.QueryRow(ctx, `
		SELECT role FROM app.organization_members
		WHERE organization_id = $1::uuid AND user_id = $2
	`, organizationID, owner).Scan(&memberRole)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", "", errors.New("schedule owner is no longer a member of the organisation")
		}
		return "", "", err
	}
	return owner, auth.NormalizeRole(memberRole), nil
}

func (s *SchedulesService) disableSchedule(ctx context.Context, scheduleID, reason string) error {
	_, err := s.appPool.Exec(ctx, `
		UPDATE app.schedules
		SET enabled = false, last_status = 'disabled', last_error = NULLIF($2, ''),
		    locked_by = NULL, locked_until = NULL, updated_at = NOW()
		WHERE id = $1
	`, scheduleID, SanitizeStoredError(errors.New(reason)))
	return err
}

// finalizeScheduleRunAfterDelivery marks a schedule_run terminal once its webhook outbox
// delivery reaches delivered/dead_letter asynchronously — i.e. after executeClaimedRun
// already returned with the run left "running" because delivery was still pending. Guarded
// so a late or duplicate outbox completion cannot clobber a run already finalized another way.
func (s *SchedulesService) finalizeScheduleRunAfterDelivery(ctx context.Context, scheduleRunID, deliveryStatus, errMsg string) {
	status := "completed"
	failureCode := ""
	message := ""
	if deliveryStatus == "dead_letter" {
		status = "dead_letter"
		failureCode = "webhook_dead_letter"
		message = firstNonBlank(errMsg, "webhook delivery dead-lettered")
	}
	_, _ = s.appPool.Exec(ctx, `
		UPDATE app.schedule_runs
		SET status = $2, failure_code = NULLIF($3, ''), failure_message = NULLIF($4, ''),
		    lease_until = NULL, completed_at = NOW()
		WHERE id = $1 AND status NOT IN ('completed', 'failed', 'dead_letter')
	`, scheduleRunID, status, failureCode, message)
}

func (s *SchedulesService) startScheduleHeartbeat(ctx context.Context, runID, scheduleID, workerID string) func() {
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(defaultScheduleLease / 3)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := s.renewScheduleLease(ctx, runID, scheduleID, workerID); err != nil {
					log.Printf("schedule lease heartbeat %s: %v", runID, err)
				}
			}
		}
	}()
	return func() { close(done) }
}

func (s *SchedulesService) renewScheduleLease(ctx context.Context, runID, scheduleID, workerID string) error {
	leaseUntil := time.Now().UTC().Add(defaultScheduleLease)
	if _, err := s.appPool.Exec(ctx, `
		UPDATE app.schedule_runs
		SET lease_until = $2, worker_id = $3
		WHERE id = $1 AND status = 'running'
	`, runID, leaseUntil, workerID); err != nil {
		return err
	}
	_, err := s.appPool.Exec(ctx, `
		UPDATE app.schedules
		SET locked_by = $2, locked_until = $3, updated_at = NOW()
		WHERE id = $1
	`, scheduleID, workerID, leaseUntil)
	return err
}

// finishScheduleRun marks a schedule_run terminal with an explicit status ("completed",
// "failed", or "dead_letter" for webhook-dead-lettered runs).
func (s *SchedulesService) finishScheduleRun(ctx context.Context, runID, reportID, status string, runErr error, failureCode string) error {
	code := failureCode
	if runErr != nil && code == "" {
		code = "execution_failed"
	}
	_, err := s.appPool.Exec(ctx, `
		UPDATE app.schedule_runs
		SET status = $2,
		    report_id = COALESCE(NULLIF($3, '')::uuid, report_id),
		    failure_code = NULLIF($4, ''),
		    failure_message = NULLIF($5, ''),
		    lease_until = NULL,
		    completed_at = NOW()
		WHERE id = $1
	`, runID, status, reportID, code, SanitizeStoredError(runErr))
	if runErr != nil {
		return runErr
	}
	return err
}
