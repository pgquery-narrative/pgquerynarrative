package service

import (
	"context"
	"fmt"
)

const defaultMaxLLMCallsPerReport = 12

type llmCallBudgetKey struct{}

type llmCallBudget struct {
	max  int
	used int
}

func withLLMCallBudget(ctx context.Context, max int) context.Context {
	if max <= 0 {
		max = defaultMaxLLMCallsPerReport
	}
	return context.WithValue(ctx, llmCallBudgetKey{}, &llmCallBudget{max: max})
}

func reserveLLMCall(ctx context.Context) error {
	budget, ok := ctx.Value(llmCallBudgetKey{}).(*llmCallBudget)
	if !ok || budget == nil {
		return nil
	}
	if budget.used >= budget.max {
		return fmt.Errorf("llm call limit exceeded for this report (%d)", budget.max)
	}
	budget.used++
	return nil
}
