package service

import (
	"context"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/gen/schedules"
)

// StartScheduleRunner polls for due schedules and executes them in the background.
// Safe for multiple replicas: uses FOR UPDATE SKIP LOCKED.
func StartScheduleRunner(ctx context.Context, appPool *pgxpool.Pool, svc *SchedulesService, interval time.Duration) {
	if svc == nil || appPool == nil || interval <= 0 {
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
				if err := svc.RunDue(ctx); err != nil {
					log.Printf("schedule runner: %v", err)
				}
			}
		}
	}()
}

// RunDue executes all enabled schedules whose next_run_at is due.
func (s *SchedulesService) RunDue(ctx context.Context) error {
	tx, err := s.appPool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, `
		SELECT id
		FROM app.schedules
		WHERE enabled = true
		  AND next_run_at IS NOT NULL
		  AND next_run_at <= NOW()
		ORDER BY next_run_at
		FOR UPDATE SKIP LOCKED
		LIMIT 10
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	for _, id := range ids {
		_, _ = s.RunNow(ctx, &schedules.RunNowPayload{ID: id})
	}
	return nil
}
