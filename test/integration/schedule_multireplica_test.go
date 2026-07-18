package integration

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/app/db"
	"github.com/pgquerynarrative/pgquerynarrative/app/service"
	"github.com/pgquerynarrative/pgquerynarrative/test/testhelpers"
)

func setupSchedulePool(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	ctx := context.Background()
	container := testhelpers.RunPostgresContainer(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for {
		pool, pingErr := pgxpool.New(waitCtx, connStr)
		if pingErr == nil {
			pingErr = pool.Ping(waitCtx)
			pool.Close()
			if pingErr == nil {
				break
			}
		}
		if waitCtx.Err() != nil {
			t.Fatal("postgres not ready")
		}
		time.Sleep(200 * time.Millisecond)
	}

	migrationsPath, err := filepath.Abs("../../app/db/migrations")
	if err != nil {
		t.Fatal(err)
	}
	m, err := migrate.New("file://"+migrationsPath, connStr)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		t.Fatal(err)
	}

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool, ctx
}

// TestScheduleClaim_MultiReplicaIdempotent verifies two workers cannot claim the same due run.
func TestScheduleClaim_MultiReplicaIdempotent(t *testing.T) {
	pool, ctx := setupSchedulePool(t)

	appDB := db.NewOrgScoped(pool)
	svcA := service.NewSchedulesService(appDB, nil, nil)
	svcA.SetRawPool(pool)
	svcB := service.NewSchedulesService(appDB, nil, nil)
	svcB.SetRawPool(pool)

	org := auth.DefaultOrganizationID
	var scheduleID string
	err := pool.QueryRow(ctx, `
		INSERT INTO app.schedules (
			name, sql, connection_id, interval_expr, destination_type, destination_target,
			enabled, next_run_at, organization_id
		) VALUES (
			'multi-replica', 'SELECT 1', 'default', '@every 5m', 'log', '',
			true, NOW() - INTERVAL '1 minute', $1::uuid
		) RETURNING id
	`, org).Scan(&scheduleID)
	if err != nil {
		t.Fatalf("insert schedule: %v", err)
	}

	if err := svcA.RunDue(ctx, "worker-a"); err != nil {
		t.Fatalf("worker-a: %v", err)
	}
	if err := svcB.RunDue(ctx, "worker-b"); err != nil {
		t.Fatalf("worker-b: %v", err)
	}

	var runCount int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM app.schedule_runs WHERE schedule_id = $1
	`, scheduleID).Scan(&runCount); err != nil {
		t.Fatal(err)
	}
	if runCount != 1 {
		t.Fatalf("expected exactly 1 schedule_run after two workers, got %d", runCount)
	}
}

// TestScheduleLeaseRecovery_TwoReplicaCrashRecovery reclaims an expired lease onto a second worker.
func TestScheduleLeaseRecovery_TwoReplicaCrashRecovery(t *testing.T) {
	pool, ctx := setupSchedulePool(t)

	appDB := db.NewOrgScoped(pool)
	svc := service.NewSchedulesService(appDB, nil, nil)
	svc.SetRawPool(pool)

	org := auth.DefaultOrganizationID
	var scheduleID, runID string
	err := pool.QueryRow(ctx, `
		INSERT INTO app.schedules (
			name, sql, connection_id, interval_expr, destination_type, destination_target,
			enabled, next_run_at, organization_id, locked_by, locked_until
		) VALUES (
			'lease-recovery', 'SELECT 1', 'default', '@every 5m', 'log', '',
			true, NOW() + INTERVAL '1 hour', $1::uuid, 'crashed-worker', NOW() - INTERVAL '1 minute'
		) RETURNING id
	`, org).Scan(&scheduleID)
	if err != nil {
		t.Fatalf("insert schedule: %v", err)
	}
	err = pool.QueryRow(ctx, `
		INSERT INTO app.schedule_runs (
			schedule_id, organization_id, scheduled_for, idempotency_key,
			worker_id, lease_until, status, attempt_count, started_at
		) VALUES (
			$1, $2::uuid, NOW() - INTERVAL '10 minutes', 'lease-recovery-key',
			'crashed-worker', NOW() - INTERVAL '1 minute', 'running', 1, NOW() - INTERVAL '10 minutes'
		) RETURNING id
	`, scheduleID, org).Scan(&runID)
	if err != nil {
		t.Fatalf("insert stuck run: %v", err)
	}

	recovered, err := svc.RecoverExpiredScheduleLeases(ctx, pool, "recovery-worker")
	if err != nil {
		t.Fatalf("RecoverExpiredScheduleLeases: %v", err)
	}
	if len(recovered) != 1 {
		t.Fatalf("expected 1 recovered run, got %d", len(recovered))
	}
	if recovered[0].RunID != runID {
		t.Fatalf("recovered run id = %s, want %s", recovered[0].RunID, runID)
	}

	var workerID string
	var attempts int
	var leaseUntil time.Time
	if err := pool.QueryRow(ctx, `
		SELECT worker_id, attempt_count, lease_until FROM app.schedule_runs WHERE id = $1
	`, runID).Scan(&workerID, &attempts, &leaseUntil); err != nil {
		t.Fatal(err)
	}
	if workerID != "recovery-worker" {
		t.Fatalf("worker_id = %q, want recovery-worker", workerID)
	}
	if attempts != 2 {
		t.Fatalf("attempt_count = %d, want 2", attempts)
	}
	if !leaseUntil.After(time.Now().UTC()) {
		t.Fatalf("expected renewed lease_until in the future, got %v", leaseUntil)
	}

	// Second recovery while lease is fresh must claim nothing.
	again, err := svc.RecoverExpiredScheduleLeases(ctx, pool, "other-worker")
	if err != nil {
		t.Fatalf("second recovery: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("expected 0 recovered runs while lease is fresh, got %d", len(again))
	}
}
