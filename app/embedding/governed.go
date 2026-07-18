package embedding

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/app/llm"
	"github.com/pgquerynarrative/pgquerynarrative/app/observability"
)

// defaultEmbedTimeout bounds how long a single embedding call may run. Embedding calls sit
// on the hot path of query save, report generation, and similar-query search; without a
// timeout a slow or hung embedding backend would stall those request paths indefinitely.
const defaultEmbedTimeout = 15 * time.Second

// defaultEmbedConcurrency caps in-flight embedding calls so a burst of saves/RAG lookups
// cannot overwhelm a local Ollama (or remote) embedding backend.
const defaultEmbedConcurrency = 8

type operationKey struct{}

// WithOperation attaches a call-site label (e.g. "embed_query_save", "embed_rag") for audit.
func WithOperation(ctx context.Context, operation string) context.Context {
	if strings.TrimSpace(operation) == "" {
		return ctx
	}
	return context.WithValue(ctx, operationKey{}, strings.TrimSpace(operation))
}

func operationFrom(ctx context.Context) string {
	if v, ok := ctx.Value(operationKey{}).(string); ok && v != "" {
		return v
	}
	return "embed"
}

// GovernedEmbedder wraps an Embedder with the same organization/user audit
// trail, external-data policy, and PII redaction applied to narrative LLM
// calls (see llm.InvokeWithBudget), plus a call timeout, concurrency limit,
// a circuit breaker (see llm.CircuitBreaker) so a failing embedding backend
// cannot stall or retry-storm the request paths that depend on it, and
// vector-dimension validation so a misconfigured or drifting backend cannot
// silently write inconsistent vectors into pgvector columns.
type GovernedEmbedder struct {
	embedder      Embedder
	audit         *llm.AuditStore
	provider      string
	allowExternal bool
	redact        bool
	timeout       time.Duration
	breaker       *llm.CircuitBreaker
	sem           chan struct{}
	// expectedDim is the vector dimension enforced on every call. 0 until the first
	// successful call, at which point it locks to that call's dimension (unless explicitly
	// set via SetExpectedDimension), so later calls that return a different dimension
	// (e.g. after an operator swaps in a different model without updating config) are
	// rejected instead of being written to storage with an inconsistent shape.
	expectedDim atomic.Int64
	initSem     sync.Once
}

// NewGovernedEmbedder wraps embedder with governance. provider identifies the
// embedding backend for policy/audit purposes (e.g. "ollama"); providers
// recognized as cloud LLM providers (see config.IsCloudLLMProvider) are
// treated as external and require allowExternal. redact applies
// llm.RedactSQL-style literal masking to the input text before it is sent,
// mirroring LLM_REDACT_PII for narrative prompts. Returns nil when embedder
// is nil so callers can treat a nil *GovernedEmbedder like a nil Embedder.
func NewGovernedEmbedder(embedder Embedder, audit *llm.AuditStore, provider string, allowExternal, redact bool) *GovernedEmbedder {
	if embedder == nil {
		return nil
	}
	if provider == "" {
		provider = "unknown"
	}
	g := &GovernedEmbedder{
		embedder:      embedder,
		audit:         audit,
		provider:      provider,
		allowExternal: allowExternal,
		redact:        redact,
		timeout:       defaultEmbedTimeout,
		breaker:       llm.NewCircuitBreaker(),
		sem:           make(chan struct{}, defaultEmbedConcurrency),
	}
	return g
}

// SetTimeout overrides the per-call embedding timeout (default 15s). d <= 0 is ignored.
func (g *GovernedEmbedder) SetTimeout(d time.Duration) {
	if g != nil && d > 0 {
		g.timeout = d
	}
}

// SetMaxConcurrency overrides the in-flight embedding call limit (default 8). n <= 0 is ignored.
func (g *GovernedEmbedder) SetMaxConcurrency(n int) {
	if g == nil || n <= 0 {
		return
	}
	g.sem = make(chan struct{}, n)
}

// SetExpectedDimension pins the vector dimension that Embed must return, rejecting any
// response of a different size. dim <= 0 clears the pin so the next successful call
// re-establishes it from its own output.
func (g *GovernedEmbedder) SetExpectedDimension(dim int) {
	if g == nil {
		return
	}
	if dim <= 0 {
		g.expectedDim.Store(0)
		return
	}
	g.expectedDim.Store(int64(dim))
}

var _ Embedder = (*GovernedEmbedder)(nil)

// Embed implements Embedder: it evaluates the same cloud/PII policy used for
// narrative LLM calls, optionally redacts literal values from text, enforces a call
// timeout, concurrency limit, and circuit breaker around the underlying provider call,
// validates the returned vector's dimension, records an audit event, and only then
// returns the vector.
func (g *GovernedEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	if g == nil || g.embedder == nil {
		return nil, fmt.Errorf("embedder is not configured")
	}
	start := time.Now()
	principal := auth.PrincipalFromContext(ctx)
	operation := operationFrom(ctx)
	gov := llm.EvaluateGovernance(llm.GovernanceInput{
		Provider:    g.provider,
		SendRowData: true,
		AllowCloud:  g.allowExternal,
		RedactPII:   g.redact,
		HasRows:     true,
		SQLText:     text,
	})
	if !gov.Allowed {
		observability.IncEmbedDenied()
		g.recordAudit(ctx, principal, gov.Decision, gov.DataClasses, text, 0, operation)
		return nil, fmt.Errorf("embedding denied: %s", gov.ErrorMessage)
	}
	if err := g.breaker.Allow(); err != nil {
		observability.IncEmbedError()
		g.recordAudit(ctx, principal, gov.Decision, gov.DataClasses, text, 0, operation)
		return nil, fmt.Errorf("embedding provider circuit open: %w", err)
	}
	if g.sem == nil {
		g.initSem.Do(func() {
			if g.sem == nil {
				g.sem = make(chan struct{}, defaultEmbedConcurrency)
			}
		})
	}
	select {
	case g.sem <- struct{}{}:
		defer func() { <-g.sem }()
	case <-ctx.Done():
		observability.IncEmbedError()
		return nil, ctx.Err()
	}
	input := text
	if g.redact {
		input = llm.RedactSQL(input)
	}
	timeout := g.timeout
	if timeout <= 0 {
		timeout = defaultEmbedTimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	vec, err := g.embedder.Embed(callCtx, input)
	decision := gov.Decision
	if err != nil {
		g.breaker.RecordFailure()
		observability.IncEmbedError()
		g.recordAudit(ctx, principal, decision, gov.DataClasses, text, 0, operation)
		return nil, err
	}
	if dimErr := g.checkDimension(len(vec)); dimErr != nil {
		g.breaker.RecordFailure()
		observability.IncEmbedError()
		g.recordAudit(ctx, principal, decision, gov.DataClasses, text, 0, operation)
		return nil, dimErr
	}
	g.breaker.RecordSuccess()
	observability.IncEmbedCall()
	observability.ObserveEmbedDuration(time.Since(start))
	g.recordAudit(ctx, principal, decision, gov.DataClasses, text, len(vec), operation)
	return vec, nil
}

// checkDimension validates got against the pinned expected dimension, establishing it from
// the first successful call when unset.
func (g *GovernedEmbedder) checkDimension(got int) error {
	if got == 0 {
		return fmt.Errorf("embedding provider returned an empty vector")
	}
	for {
		want := g.expectedDim.Load()
		if want == 0 {
			if g.expectedDim.CompareAndSwap(0, int64(got)) {
				return nil
			}
			continue
		}
		if int(want) != got {
			return fmt.Errorf("embedding dimension mismatch: expected %d, got %d", want, got)
		}
		return nil
	}
}

func (g *GovernedEmbedder) recordAudit(ctx context.Context, principal auth.Principal, decision llm.PolicyDecision, classes []llm.DataClass, text string, vectorLen int, operation string) {
	if g.audit == nil {
		return
	}
	g.audit.Record(ctx, llm.AuditEvent{
		OrganizationID:   principal.OrgID,
		UserID:           principal.UserID,
		Provider:         g.provider,
		Model:            "embedding",
		Operation:        operation,
		PolicyDecision:   decision,
		DataClasses:      llm.FormatDataClasses(classes),
		SendRowData:      true,
		RedactPII:        g.redact,
		AllowExternal:    g.allowExternal,
		PromptTokens:     llm.EstimateTokenCount(text),
		CompletionTokens: vectorLen,
	})
}
