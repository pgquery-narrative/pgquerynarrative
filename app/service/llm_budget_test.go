package service

import (
	"context"
	"testing"
)

func TestLLMCallBudget_enforcesCap(t *testing.T) {
	ctx := withLLMCallBudget(context.Background(), 2)
	if err := reserveLLMCall(ctx); err != nil {
		t.Fatalf("first reserve: %v", err)
	}
	if err := reserveLLMCall(ctx); err != nil {
		t.Fatalf("second reserve: %v", err)
	}
	if err := reserveLLMCall(ctx); err == nil {
		t.Fatal("expected third reserve to fail")
	}
}
