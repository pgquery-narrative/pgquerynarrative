package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
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
				if err := svc.RecoverExpiredScheduleLeases(ctx, rawPool, workerID); err != nil {
					log.Printf("schedule lease recovery: %v", err)
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
	runCtx := auth.WithPrincipal(ctx, auth.Principal{UserID: workerID, OrgID: claim.OrgID, Role: auth.RoleAdmin})
	if s.reportsSvc == nil {
		return s.finishScheduleRun(runCtx, claim.RunID, "", errors.New("reports service not configured"), "misconfigured")
	}
	_ = s.renewScheduleLease(runCtx, claim.RunID, workerID)
	sc, err := s.getByIDForOrg(runCtx, claim.ScheduleID, claim.OrgID)
	if err != nil {
		return s.finishScheduleRun(runCtx, claim.RunID, "", err, "schedule_not_found")
	}
	reportID, delivered, runErr := s.runSchedule(runCtx, sc)
	status := "completed"
	if runErr != nil {
		status = "failed"
	}
	nextRun, _ := computeNextRun(sc.CronExpr, time.Now().UTC())
	_, _ = s.appPool.Exec(runCtx, `
		UPDATE app.schedules
		SET last_run_at = NOW(), last_status = $2, last_error = NULLIF($3, ''),
		    next_run_at = $4, locked_by = NULL, locked_until = NULL, updated_at = NOW()
		WHERE id = $1
	`, sc.ID, status, SanitizeStoredError(runErr), nextRun)
	_ = delivered
	return s.finishScheduleRun(runCtx, claim.RunID, reportID, runErr, "")
}

func (s *SchedulesService) renewScheduleLease(ctx context.Context, runID, workerID string) error {
	leaseUntil := time.Now().UTC().Add(defaultScheduleLease)
	_, err := s.appPool.Exec(ctx, `
		UPDATE app.schedule_runs
		SET lease_until = $2, worker_id = $3
		WHERE id = $1 AND status = 'running'
	`, runID, leaseUntil, workerID)
	return err
}

func (s *SchedulesService) finishScheduleRun(ctx context.Context, runID, reportID string, runErr error, failureCode string) error {
	status := "completed"
	code := failureCode
	if runErr != nil {
		status = "failed"
		if code == "" {
			code = "execution_failed"
		}
	}
	_, err := s.appPool.Exec(ctx, `
		UPDATE app.schedule_runs
		SET status = $2,
		    report_id = NULLIF($3, '')::uuid,
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
