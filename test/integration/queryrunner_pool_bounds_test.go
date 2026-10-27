package integration

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/internal/queryrunner"
	"github.com/pgquerynarrative/pgquerynarrative/test/helpers"
)

// TestRunPoolAndTimeoutBounds covers Runner.Run's acquisition and execution
// budgets: every path must return in bounded time, not hang, and a
// subsequent caller must still be served once the contention clears.
func TestRunPoolAndTimeoutBounds(t *testing.T) {
	ctx := context.Background()
	container := helpers.RunPostgresContainer(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })
	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	waitReady(t, ctx, connStr)

	cfg, err := pgxpool.ParseConfig(connStr)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	validator := queryrunner.NewValidator(nil, 10000)
	const queryLimit = 3 * time.Second
	runner := queryrunner.NewRunner(pool, validator, 10, queryLimit)

	t.Run("pool exhaustion times out instead of hanging forever", func(t *testing.T) {
		// Hold the pool's one connection for longer than queryLimit, outside
		// the runner entirely, so Run's own acquire has nothing free to get.
		held, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		release := time.AfterFunc(2*queryLimit, func() { held.Release() })
		defer func() {
			if release.Stop() {
				held.Release()
			}
		}()

		start := time.Now()
		_, err = runner.Run(ctx, "SELECT 1", 10)
		elapsed := time.Since(start)

		if err == nil {
			t.Fatal("expected an error when the pool has no free connection")
		}
		if elapsed > queryLimit+2*time.Second {
			t.Fatalf("Run took %v to fail — acquisition did not respect its own ~%v bound", elapsed, queryLimit)
		}
		if elapsed < queryLimit/2 {
			t.Fatalf("Run failed after only %v — suspiciously fast for a bounded wait of ~%v; check it actually waited rather than failing immediately for an unrelated reason", elapsed, queryLimit)
		}
		if !strings.Contains(err.Error(), "timeout") && !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected a timeout-classified error, got: %v", err)
		}
	})

	t.Run("cancellation while waiting for a connection returns promptly", func(t *testing.T) {
		held, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		release := time.AfterFunc(2*queryLimit, func() { held.Release() })
		defer func() {
			if release.Stop() {
				held.Release()
			}
		}()

		cancelAfter := 300 * time.Millisecond
		cctx, cancel := context.WithTimeout(ctx, cancelAfter)
		defer cancel()

		start := time.Now()
		_, err = runner.Run(cctx, "SELECT 1", 10)
		elapsed := time.Since(start)

		if err == nil {
			t.Fatal("expected an error when the caller's own context expires first")
		}
		// Generous margin over cancelAfter for scheduling jitter, but far
		// short of queryLimit — proving the wait responded to cancellation
		// rather than running out its own full bound regardless.
		if elapsed > cancelAfter+2*time.Second {
			t.Fatalf("Run took %v after a %v caller cancellation — did not respect caller cancellation while waiting for a connection", elapsed, cancelAfter)
		}
	})

	t.Run("cancellation during execution returns promptly, pool recovers", func(t *testing.T) {
		cancelAfter := 300 * time.Millisecond
		cctx, cancel := context.WithTimeout(ctx, cancelAfter)
		defer cancel()

		start := time.Now()
		// A deliberately expensive query (no pg_sleep — denied by the
		// validator) that will not finish before the context above expires.
		_, err := runner.Run(cctx, "SELECT count(*) FROM generate_series(1, 2000000000)", 10)
		elapsed := time.Since(start)

		if err == nil {
			t.Fatal("expected the slow query to be cancelled before completion")
		}
		if elapsed > cancelAfter+2*time.Second {
			t.Fatalf("Run took %v after a %v caller cancellation during execution — did not respect it", elapsed, cancelAfter)
		}

		// The pool must be usable again immediately — the cancelled query's
		// connection was discarded (see discardConn), not left stuck.
		start2 := time.Now()
		result, err := runner.Run(ctx, "SELECT 1", 10)
		if err != nil {
			t.Fatalf("Run after cancellation: %v", err)
		}
		if elapsed2 := time.Since(start2); elapsed2 > queryLimit {
			t.Fatalf("recovery Run took %v, pool did not recover promptly", elapsed2)
		}
		if result.RowCount != 1 {
			t.Fatalf("recovery Run returned %d rows, want 1", result.RowCount)
		}
	})

	// RunSnapshot's setup (BeginTx, replay) and teardown (Commit/Rollback)
	// steps each get their own bounded context now, not the caller's bare
	// one — a review flagged that they previously did not, which would have
	// let a deadline-less caller block forever on any of those steps if the
	// server stalled (only the per-query loop was bounded). This uses
	// context.Background() specifically — not a context with its own
	// generous deadline — to prove the "no deadline at all" case: the one
	// scenario a caller-supplied timeout cannot mask.
	t.Run("RunSnapshot with no caller deadline still bounds pool exhaustion", func(t *testing.T) {
		held, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		release := time.AfterFunc(2*queryLimit, func() { held.Release() })
		defer func() {
			if release.Stop() {
				held.Release()
			}
		}()

		start := time.Now()
		results := runner.RunSnapshot(context.Background(), []queryrunner.SnapshotQuery{{SQL: "SELECT 1", Limit: 10}})
		elapsed := time.Since(start)

		if len(results) != 1 || results[0].Err == nil {
			t.Fatalf("expected an error when the pool has no free connection, got %+v", results)
		}
		if elapsed > queryLimit+2*time.Second {
			t.Fatalf("RunSnapshot(context.Background()) took %v to fail — acquisition did not respect its own ~%v bound even with no caller deadline", elapsed, queryLimit)
		}
	})
}
