package audit

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func unreachablePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig("postgres://audit_test:audit_test@127.0.0.1:1/audit_test?connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestModeRequired_HighRiskWriteFailureReturned(t *testing.T) {
	pool := unreachablePool(t)
	s := NewStore(pool, ModeRequired)
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := s.Record(ctx, Entry{
		EventType: EventRunQuery,
		HighRisk:  true,
		UserID:    "u1",
		OrgID:     "00000000-0000-0000-0000-000000000001",
	})
	if err == nil {
		t.Fatal("expected ModeRequired HighRisk Record to return write error")
	}
}

func TestModeRequired_NonHighRiskWriteFailureSwallowed(t *testing.T) {
	pool := unreachablePool(t)
	s := NewStore(pool, ModeRequired)
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.Record(ctx, Entry{
		EventType: EventAPIRequest,
		HighRisk:  false,
		UserID:    "u1",
	}); err != nil {
		t.Fatalf("non-high-risk ModeRequired should swallow write errors, got %v", err)
	}
}

func TestModeBestEffort_WriteFailureNeverReturned(t *testing.T) {
	pool := unreachablePool(t)
	s := NewStore(pool, ModeBestEffort)
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.Record(ctx, Entry{
		EventType: EventRunQuery,
		HighRisk:  true,
	}); err != nil {
		t.Fatalf("ModeBestEffort must never return write errors, got %v", err)
	}
}

func TestModeBuffered_EnqueueNeverBlocksOrFails(t *testing.T) {
	pool := unreachablePool(t)
	s := NewStore(pool, ModeBuffered)
	defer s.Close()
	if s.queue == nil {
		t.Fatal("ModeBuffered with pool should create a queue")
	}

	ctx := context.Background()
	// Overflow the in-memory queue so spill-to-buffer path is exercised (DB write will fail).
	for i := 0; i < bufferQueueSize+5; i++ {
		if err := s.Record(ctx, Entry{EventType: EventAPIRequest, UserID: "u"}); err != nil {
			t.Fatalf("ModeBuffered Record must not fail, got %v on iteration %d", err, i)
		}
	}
}

func TestModeBuffered_CloseDrainsWithoutPanic(t *testing.T) {
	pool := unreachablePool(t)
	s := NewStore(pool, ModeBuffered)
	_ = s.Record(context.Background(), Entry{EventType: EventAuthSuccess, UserID: "u"})
	done := make(chan struct{})
	go func() {
		s.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return within timeout")
	}
}
