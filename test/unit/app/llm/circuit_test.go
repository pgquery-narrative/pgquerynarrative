package llm_test

import (
	"testing"

	"github.com/pgquerynarrative/pgquerynarrative/app/llm"
)

func TestCircuitBreakerOpensAfterFailures(t *testing.T) {
	cb := llm.NewCircuitBreaker()
	for i := 0; i < 5; i++ {
		cb.RecordFailure()
	}
	if err := cb.Allow(); err == nil {
		t.Fatal("expected circuit to open after threshold failures")
	}
	cb.RecordSuccess()
	if err := cb.Allow(); err != nil {
		t.Fatalf("expected circuit to close after success: %v", err)
	}
}
