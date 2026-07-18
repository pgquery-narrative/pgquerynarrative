package embedding

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeEmbedder is a test double for Embedder with configurable behavior.
type fakeEmbedder struct {
	vec     []float32
	err     error
	delay   time.Duration
	calls   int
	lastReq string
}

func (f *fakeEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	f.calls++
	f.lastReq = text
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.vec, nil
}

func TestGovernedEmbedder_NilWhenEmbedderNil(t *testing.T) {
	if g := NewGovernedEmbedder(nil, nil, "ollama", false, true); g != nil {
		t.Fatal("expected nil GovernedEmbedder when wrapped embedder is nil")
	}
}

func TestGovernedEmbedder_AllowsLocalProvider(t *testing.T) {
	fake := &fakeEmbedder{vec: []float32{0.1, 0.2, 0.3}}
	g := NewGovernedEmbedder(fake, nil, "ollama", false, true)
	vec, err := g.Embed(context.Background(), "SELECT * FROM demo.orders WHERE id = 42")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vec) != 3 {
		t.Fatalf("expected vector of length 3, got %d", len(vec))
	}
	if fake.calls != 1 {
		t.Fatalf("expected 1 call to underlying embedder, got %d", fake.calls)
	}
}

func TestGovernedEmbedder_DeniesCloudProviderWithoutApproval(t *testing.T) {
	fake := &fakeEmbedder{vec: []float32{0.1, 0.2}}
	g := NewGovernedEmbedder(fake, nil, "openai", false, true)
	_, err := g.Embed(context.Background(), "SELECT 1")
	if err == nil {
		t.Fatal("expected denial for cloud provider without external-data approval")
	}
	if fake.calls != 0 {
		t.Fatalf("underlying embedder should not be called when governance denies the request, got %d calls", fake.calls)
	}
}

func TestGovernedEmbedder_AllowsCloudProviderWithApproval(t *testing.T) {
	fake := &fakeEmbedder{vec: []float32{0.1, 0.2}}
	g := NewGovernedEmbedder(fake, nil, "openai", true, true)
	_, err := g.Embed(context.Background(), "SELECT 1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGovernedEmbedder_RedactsSQLBeforeSending(t *testing.T) {
	fake := &fakeEmbedder{vec: []float32{0.1, 0.2}}
	g := NewGovernedEmbedder(fake, nil, "ollama", false, true)
	_, err := g.Embed(context.Background(), "SELECT * FROM demo.customers WHERE email = 'jane@example.com'")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(fake.lastReq, "jane@example.com") {
		t.Fatalf("expected literal to be redacted before sending, got %q", fake.lastReq)
	}
}

func TestGovernedEmbedder_RejectsDimensionMismatch(t *testing.T) {
	fake := &fakeEmbedder{vec: []float32{0.1, 0.2, 0.3}}
	g := NewGovernedEmbedder(fake, nil, "ollama", false, false)
	if _, err := g.Embed(context.Background(), "first call"); err != nil {
		t.Fatalf("unexpected error establishing baseline dimension: %v", err)
	}
	fake.vec = []float32{0.1, 0.2} // different dimension on second call
	if _, err := g.Embed(context.Background(), "second call"); err == nil {
		t.Fatal("expected dimension-mismatch error on second call")
	}
}

func TestGovernedEmbedder_SetExpectedDimensionRejectsFirstCall(t *testing.T) {
	fake := &fakeEmbedder{vec: []float32{0.1, 0.2, 0.3}}
	g := NewGovernedEmbedder(fake, nil, "ollama", false, false)
	g.SetExpectedDimension(1536)
	if _, err := g.Embed(context.Background(), "text"); err == nil {
		t.Fatal("expected dimension-mismatch error against pinned expected dimension")
	}
}

func TestGovernedEmbedder_RejectsEmptyVector(t *testing.T) {
	fake := &fakeEmbedder{vec: []float32{}}
	g := NewGovernedEmbedder(fake, nil, "ollama", false, false)
	if _, err := g.Embed(context.Background(), "text"); err == nil {
		t.Fatal("expected error for empty vector")
	}
}

func TestGovernedEmbedder_TimesOutOnSlowBackend(t *testing.T) {
	fake := &fakeEmbedder{vec: []float32{0.1}, delay: 100 * time.Millisecond}
	g := NewGovernedEmbedder(fake, nil, "ollama", false, false)
	g.SetTimeout(10 * time.Millisecond)
	_, err := g.Embed(context.Background(), "text")
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestGovernedEmbedder_CircuitBreakerOpensAfterRepeatedFailures(t *testing.T) {
	fake := &fakeEmbedder{err: errors.New("backend unavailable")}
	g := NewGovernedEmbedder(fake, nil, "ollama", false, false)
	var lastErr error
	for i := 0; i < 10; i++ {
		_, lastErr = g.Embed(context.Background(), "text")
	}
	if lastErr == nil {
		t.Fatal("expected an error after repeated backend failures")
	}
	callsAfterFailures := fake.calls
	// Once the breaker opens it should short-circuit without calling the backend again.
	if _, err := g.Embed(context.Background(), "text"); err == nil {
		t.Fatal("expected circuit-open error")
	}
	if fake.calls != callsAfterFailures {
		t.Fatalf("expected breaker to short-circuit the call (calls stayed at %d), got %d", callsAfterFailures, fake.calls)
	}
}
