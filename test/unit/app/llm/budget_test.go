package llm_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/app/llm"
)

func TestBudgetStore_DisabledAllows(t *testing.T) {
	b := llm.NewBudgetStore(nil, llm.BudgetConfig{DailyTokenLimit: 100})
	if b.Enabled() {
		t.Fatal("nil pool should disable budget store")
	}
}

// unreachablePool returns a pool that parses successfully but cannot connect
// (pgxpool dials lazily), so budget operations exercise real ledger-error
// handling without requiring Docker/Postgres.
func unreachablePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig("postgres://user:pass@127.0.0.1:1/nonexistent?connect_timeout=1")
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	return pool
}

func TestBudgetStore_ReserveNoopWhenDisabled(t *testing.T) {
	pool := unreachablePool(t)
	defer pool.Close()
	// No limits configured: Enabled() is false, so Reserve must not touch the DB.
	b := llm.NewBudgetStore(pool, llm.BudgetConfig{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	id, err := b.Reserve(ctx, "org", "user", 100)
	if err != nil {
		t.Fatalf("expected no error when disabled, got %v", err)
	}
	if id != "" {
		t.Fatalf("expected empty reservation id when disabled, got %q", id)
	}
}

func TestBudgetStore_ReserveFailClosedOnLedgerError(t *testing.T) {
	pool := unreachablePool(t)
	defer pool.Close()
	b := llm.NewBudgetStore(pool, llm.BudgetConfig{DailyTokenLimit: 1000, FailClosed: true})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := b.Reserve(ctx, "org", "user", 100)
	if err == nil {
		t.Fatal("expected fail-closed error when ledger is unreachable")
	}
	if !errors.Is(err, llm.ErrBudgetLedgerUnavailable) {
		t.Fatalf("expected ErrBudgetLedgerUnavailable, got %v", err)
	}
}

func TestBudgetStore_ReserveFailsOpenByDefault(t *testing.T) {
	pool := unreachablePool(t)
	defer pool.Close()
	b := llm.NewBudgetStore(pool, llm.BudgetConfig{DailyTokenLimit: 1000}) // FailClosed defaults false
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	id, err := b.Reserve(ctx, "org", "user", 100)
	if err != nil {
		t.Fatalf("expected fail-open (nil error) when ledger unreachable, got %v", err)
	}
	if id != "" {
		t.Fatalf("expected empty reservation id on fail-open path, got %q", id)
	}
}

func TestBudgetStore_ReleaseAndReconcileNoopWithoutID(t *testing.T) {
	pool := unreachablePool(t)
	defer pool.Close()
	b := llm.NewBudgetStore(pool, llm.BudgetConfig{DailyTokenLimit: 1000})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Should not panic or block even though the ledger is unreachable and id is empty.
	b.ReleaseReservation(ctx, "", "org")
	b.ReconcileUsage(ctx, "", "org", "user", 10, 10)
}

func TestEstimateTokenCount(t *testing.T) {
	if llm.EstimateTokenCount("") != 0 {
		t.Fatal("empty")
	}
	n := llm.EstimateTokenCount("abcd")
	if n != 1 {
		t.Fatalf("got %d", n)
	}
}

func TestInvokeWithBudget_DeniesCloudWithoutApproval(t *testing.T) {
	client := stubLLM{name: "openai", resp: "ok"}
	_, err := llm.InvokeWithBudget(context.Background(), client, llm.InvokeOptions{}, "test", llm.GovernanceInput{
		Provider:   "openai",
		AllowCloud: false,
	}, "hello")
	if err == nil {
		t.Fatal("expected deny")
	}
}

type stubLLM struct {
	name string
	resp string
}

func (s stubLLM) Generate(context.Context, string) (string, error) { return s.resp, nil }
func (s stubLLM) Name() string                                     { return s.name }
