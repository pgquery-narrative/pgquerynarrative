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

// TestScheduleClaim_MultiReplicaIdempotent verifies two workers cannot claim the same due run.
func TestScheduleClaim_MultiReplicaIdempotent(t *testing.T) {
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
	defer pool.Close()

	appDB := db.NewOrgScoped(pool)
	svcA := service.NewSchedulesService(appDB, nil, nil)
	svcA.SetRawPool(pool)
	svcB := service.NewSchedulesService(appDB, nil, nil)
	svcB.SetRawPool(pool)

	org := auth.DefaultOrganizationID
	var scheduleID string
	err = pool.QueryRow(ctx, `
		INSERT INTO app.schedules (
			name, sql, connection_id, cron_expr, destination_type, destination_target,
			enabled, next_run_at, organization_id
		) VALUES (
			'multi-replica', 'SELECT 1', 'default', '*/5 * * * *', 'log', '',
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
