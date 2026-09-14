package integration

import (
	"context"
	"path/filepath"
	"sync"
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

// TestScheduleClaim_SkipsRowLockedByConcurrentClaim proves the actual
// contention mechanism — claimDueSchedules' `FOR UPDATE SKIP LOCKED` — rather
// than the two sequential RunDue calls above, which never have two
// transactions open at once and so never exercise row-lock contention at all:
// worker A commits and releases the row before worker B's SELECT ever runs.
// Here a held lock (simulating an in-flight claim from another replica)
// forces worker B's claim query to run while the row is genuinely locked.
func TestScheduleClaim_SkipsRowLockedByConcurrentClaim(t *testing.T) {
	pool, ctx := setupSchedulePool(t)

	appDB := db.NewOrgScoped(pool)
	svc := service.NewSchedulesService(appDB, nil, nil)
	svc.SetRawPool(pool)

	org := auth.DefaultOrganizationID
	var scheduleID string
	err := pool.QueryRow(ctx, `
		INSERT INTO app.schedules (
			name, sql, connection_id, interval_expr, destination_type, destination_target,
			enabled, next_run_at, organization_id
		) VALUES (
			'lock-contention', 'SELECT 1', 'default', '@every 5m', 'log', '',
			true, NOW() - INTERVAL '1 minute', $1::uuid
		) RETURNING id
	`, org).Scan(&scheduleID)
	if err != nil {
		t.Fatalf("insert schedule: %v", err)
	}

	// Hold the row locked on a separate connection, exactly as a concurrent
	// replica's open claimDueSchedules transaction would.
	holder, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire holder connection: %v", err)
	}
	defer holder.Release()
	holdTx, err := holder.Begin(ctx)
	if err != nil {
		t.Fatalf("begin holder tx: %v", err)
	}
	if _, err := holdTx.Exec(ctx, `SELECT 1 FROM app.schedules WHERE id = $1 FOR UPDATE`, scheduleID); err != nil {
		t.Fatalf("lock schedule row: %v", err)
	}

	// While the row is locked, a worker running RunDue must skip it — not
	// block waiting for the lock, and not error.
	if err := svc.RunDue(ctx, "worker-b"); err != nil {
		t.Fatalf("RunDue while row locked: %v", err)
	}
	var runCountWhileLocked int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM app.schedule_runs WHERE schedule_id = $1`, scheduleID).Scan(&runCountWhileLocked); err != nil {
		t.Fatal(err)
	}
	if runCountWhileLocked != 0 {
		t.Fatalf("SKIP LOCKED should have skipped the held row, got %d runs", runCountWhileLocked)
	}

	// Release the lock (as if the other replica's claim transaction
	// committed or rolled back) and confirm the schedule becomes claimable.
	if err := holdTx.Rollback(ctx); err != nil {
		t.Fatalf("rollback holder tx: %v", err)
	}
	if err := svc.RunDue(ctx, "worker-b"); err != nil {
		t.Fatalf("RunDue after lock released: %v", err)
	}
	var runCountAfterRelease int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM app.schedule_runs WHERE schedule_id = $1`, scheduleID).Scan(&runCountAfterRelease); err != nil {
		t.Fatal(err)
	}
	if runCountAfterRelease != 1 {
		t.Fatalf("expected exactly 1 schedule_run once the lock was released, got %d", runCountAfterRelease)
	}
}

// TestScheduleClaim_ConcurrentRunDueClaimsExactlyOnce launches both workers'
// RunDue calls as real, interleaving goroutines (rather than sequential calls)
// so the transactions genuinely race on the same row, in addition to the
// deterministic lock-holding test above.
func TestScheduleClaim_ConcurrentRunDueClaimsExactlyOnce(t *testing.T) {
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
			'concurrent-claim', 'SELECT 1', 'default', '@every 5m', 'log', '',
			true, NOW() - INTERVAL '1 minute', $1::uuid
		) RETURNING id
	`, org).Scan(&scheduleID)
	if err != nil {
		t.Fatalf("insert schedule: %v", err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		errs <- svcA.RunDue(ctx, "worker-a")
	}()
	go func() {
		defer wg.Done()
		errs <- svcB.RunDue(ctx, "worker-b")
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("RunDue: %v", err)
		}
	}

	var runCount int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM app.schedule_runs WHERE schedule_id = $1
	`, scheduleID).Scan(&runCount); err != nil {
		t.Fatal(err)
	}
	if runCount != 1 {
		t.Fatalf("expected exactly 1 schedule_run from two truly concurrent workers, got %d", runCount)
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
