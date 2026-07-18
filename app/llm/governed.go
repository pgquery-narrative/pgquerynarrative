package llm

import (
	"context"
	"errors"
)

// GovernedClient bundles a raw provider Client with the audit store, budget
// store, and external-data policy it must honor on every call. It exists so
// callers (ReportsService, AskService, story.Generator, ...) can hold a single
// value wired once at construction time instead of three separate fields
// (audit store, budget store, allow-cloud flag) plus the raw client — see
// app/service.GovernedAI for the narrow interface this satisfies.
//
// GovernedClient is safe for concurrent use; all state is immutable after
// construction via NewGovernedClient.
type GovernedClient struct {
	client     Client
	audit      *AuditStore
	budget     *BudgetStore
	allowCloud bool
}

// NewGovernedClient builds a GovernedClient. audit and budget may be nil to
// disable those checks (e.g. in tests, or when no app pool is configured).
func NewGovernedClient(client Client, audit *AuditStore, budget *BudgetStore, allowCloud bool) *GovernedClient {
	return &GovernedClient{client: client, audit: audit, budget: budget, allowCloud: allowCloud}
}

// Invoke evaluates governance policy, checks/reserves budget, invokes the
// underlying provider, and records an audit event — see InvokeWithBudget.
// operation identifies the call site (e.g. "narrative_generate", "nl2sql")
// for audit and observability purposes.
func (g *GovernedClient) Invoke(ctx context.Context, operation string, gov GovernanceInput, prompt string) (string, error) {
	if g == nil {
		return "", errGovernedClientNotConfigured
	}
	return InvokeWithBudget(ctx, g.client, InvokeOptions{Audit: g.audit, Budget: g.budget}, operation, gov, prompt)
}

// Provider returns the underlying LLM provider name (e.g. "ollama", "claude"),
// empty when no client is configured.
func (g *GovernedClient) Provider() string {
	if g == nil || g.client == nil {
		return ""
	}
	return g.client.Name()
}

// AllowExternalData reports whether this GovernedClient is permitted to send
// data to external/cloud LLM providers.
func (g *GovernedClient) AllowExternalData() bool {
	return g != nil && g.allowCloud
}

var errGovernedClientNotConfigured = errors.New("llm client is not configured")
