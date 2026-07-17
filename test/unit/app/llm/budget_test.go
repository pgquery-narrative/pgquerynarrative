package llm_test

import (
	"context"
	"testing"

	"github.com/pgquerynarrative/pgquerynarrative/app/llm"
)

func TestBudgetStore_DisabledAllows(t *testing.T) {
	b := llm.NewBudgetStore(nil, llm.BudgetConfig{DailyTokenLimit: 100})
	if b.Enabled() {
		t.Fatal("nil pool should disable budget store")
	}
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
