package integration

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/app/llm"
	"github.com/pgquerynarrative/pgquerynarrative/test/testhelpers"
)

// TestBudgetReserve_ConcurrentNearLimit verifies that concurrent Reserve calls
// near an org daily token limit cannot all succeed: the advisory-lock-guarded
// check-then-insert must serialize so the sum of committed reservations never
// exceeds the configured budget, even under a race.
func TestBudgetReserve_ConcurrentNearLimit(t *testing.T) {
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

	// Budget allows only 500 tokens/day for the org. Each reservation asks
	// for 100 tokens, so at most 5 of 20 concurrent requests may succeed.
	const tokenLimit = 500
	const perRequestTokens = 100
	const concurrency = 20
	const maxExpectedSuccesses = tokenLimit / perRequestTokens

	budget := llm.NewBudgetStore(pool, llm.BudgetConfig{DailyTokenLimit: tokenLimit})
	org := auth.DefaultOrganizationID

	var successes atomic.Int64
	var denials atomic.Int64
	requestIDs := make([]string, concurrency)
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			id, err := budget.Reserve(ctx, org, "concurrent-user", perRequestTokens)
			if err != nil || id == "" {
				denials.Add(1)
				return
			}
			requestIDs[idx] = id
			successes.Add(1)
		}(i)
	}
	wg.Wait()

	if got := successes.Load(); got > int64(maxExpectedSuccesses) {
		t.Fatalf("expected at most %d successful reservations, got %d", maxExpectedSuccesses, got)
	}
	if got := successes.Load() + denials.Load(); got != concurrency {
		t.Fatalf("expected %d total attempts accounted for, got %d", concurrency, got)
	}

	// Sum of active reservations must never exceed the configured limit.
	var reservedTokens int64
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(reserved_tokens), 0) FROM app.llm_budget_reservations
		WHERE organization_id = $1::uuid AND status = 'reserved'
	`, org).Scan(&reservedTokens); err != nil {
		t.Fatal(err)
	}
	if reservedTokens > tokenLimit {
		t.Fatalf("reserved tokens %d exceed limit %d", reservedTokens, tokenLimit)
	}

	// Reconciling a reservation moves it out of the active pool and into committed usage.
	for _, id := range requestIDs {
		if id == "" {
			continue
		}
		budget.ReconcileUsage(ctx, id, org, "concurrent-user", 40, 40)
	}
	var activeAfterReconcile int64
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM app.llm_budget_reservations
		WHERE organization_id = $1::uuid AND status = 'reserved'
	`, org).Scan(&activeAfterReconcile); err != nil {
		t.Fatal(err)
	}
	if activeAfterReconcile != 0 {
		t.Fatalf("expected no active reservations after reconcile, got %d", activeAfterReconcile)
	}
}

// TestBudgetReserve_ReleaseAndExpire verifies Release frees a reservation
// immediately and ExpireAbandoned reclaims reservations left behind past TTL.
func TestBudgetReserve_ReleaseAndExpire(t *testing.T) {
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

	org := auth.DefaultOrganizationID
	budget := llm.NewBudgetStore(pool, llm.BudgetConfig{DailyTokenLimit: 1000, ReservationTTL: 50 * time.Millisecond})

	id, err := budget.Reserve(ctx, org, "release-user", 100)
	if err != nil || id == "" {
		t.Fatalf("expected reservation, err=%v id=%q", err, id)
	}
	budget.ReleaseReservation(ctx, id, org)
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM app.llm_budget_reservations WHERE request_id = $1::uuid`, id).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "released" {
		t.Fatalf("expected released, got %s", status)
	}

	id2, err := budget.Reserve(ctx, org, "expire-user", 100)
	if err != nil || id2 == "" {
		t.Fatalf("expected reservation, err=%v id=%q", err, id2)
	}
	time.Sleep(100 * time.Millisecond)
	n, err := budget.ExpireAbandoned(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n < 1 {
		t.Fatalf("expected at least 1 expired reservation, got %d", n)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM app.llm_budget_reservations WHERE request_id = $1::uuid`, id2).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "expired" {
		t.Fatalf("expected expired, got %s", status)
	}
}
