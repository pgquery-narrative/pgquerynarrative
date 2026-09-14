package service

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestStartScheduleRunner_TickerLoopSurvivesPanic proves the actual goroutine
// started by StartScheduleRunner keeps running after a tick panics — not just
// that recoverWorkerPanic recovers a panic in isolation (worker_recover_test.go
// already covers that). A regression here (e.g. someone moving the recover
// out of runScheduleTick, or wrapping it around the wrong scope) would kill
// the scheduler for every org on the very first bad row, and only a test that
// exercises the real ticker loop would catch it.
func TestStartScheduleRunner_TickerLoopSurvivesPanic(t *testing.T) {
	originalTickFn := scheduleTickFn
	t.Cleanup(func() { scheduleTickFn = originalTickFn })

	var ticks atomic.Int32
	scheduleTickFn = func(ctx context.Context, svc *SchedulesService, rawPool *pgxpool.Pool, workerID string) {
		defer recoverWorkerPanic("schedule_runner_test")
		n := ticks.Add(1)
		if n == 1 {
			panic("simulated panic on the first tick")
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// StartScheduleRunner only requires non-nil svc/rawPool to pass its own
	// guard; scheduleTickFn is fully overridden above, so neither is ever
	// dereferenced — no real database connection is needed for this test.
	StartScheduleRunner(ctx, &pgxpool.Pool{}, &SchedulesService{}, 10*time.Millisecond)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ticks.Load() >= 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	got := ticks.Load()
	if got < 3 {
		t.Fatalf("expected at least 3 ticks (1 panicking + at least 2 recovered) within 2s, got %d — the ticker loop likely died after the panic", got)
	}
}
