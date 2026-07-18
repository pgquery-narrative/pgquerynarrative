package llm

import (
	"context"
	"fmt"
	"time"

	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/app/observability"
)

// defaultMaxOutputTokenEstimate mirrors the max_tokens/maxOutputTokens cap
// providers are configured with (see claude.go, gemini.go, groq.go, openai.go).
// Reservations use prompt tokens plus this allowance so budgets account for
// worst-case completion size before the response is known.
const defaultMaxOutputTokenEstimate = 2048

// InvokeOptions configures an LLM invocation beyond governance input.
type InvokeOptions struct {
	Audit  *AuditStore
	Budget *BudgetStore
}

// Invoke evaluates governance policy, budgets, audits the attempt, and calls the LLM provider.
func Invoke(ctx context.Context, client Client, audit *AuditStore, operation string, in GovernanceInput, prompt string) (string, error) {
	return InvokeWithBudget(ctx, client, InvokeOptions{Audit: audit}, operation, in, prompt)
}

// InvokeWithBudget is like Invoke but enforces optional daily/monthly token
// and cost budgets. Budget enforcement is atomic: tokens are reserved
// (prompt estimate + max output allowance) before the provider call via
// BudgetStore.Reserve, reconciled to actual usage on success, and released
// on failure, so concurrent requests near a limit cannot all pass a
// stale check.
func InvokeWithBudget(ctx context.Context, client Client, opts InvokeOptions, operation string, in GovernanceInput, prompt string) (string, error) {
	if client == nil {
		return "", fmt.Errorf("llm client is not configured")
	}
	in.Provider = client.Name()
	gov := EvaluateGovernance(in)
	provider, model := metadata(client)
	principal := auth.PrincipalFromContext(ctx)
	promptTokens := EstimateTokenCount(prompt)

	recordAudit := func(decision PolicyDecision, classes []DataClass, latencyMs, completionTokens int, costUSD float64) {
		if opts.Audit == nil {
			return
		}
		ev := AuditEvent{
			OrganizationID:   principal.OrgID,
			UserID:           principal.UserID,
			Provider:         provider,
			Model:            model,
			Operation:        operation,
			PolicyDecision:   decision,
			DataClasses:      FormatDataClasses(classes),
			SendRowData:      in.SendRowData,
			RedactPII:        in.RedactPII,
			AllowExternal:    in.AllowCloud,
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens,
			LatencyMs:        latencyMs,
			EstimatedCostUSD: costUSD,
		}
		opts.Audit.Record(ctx, ev)
	}

	if !gov.Allowed {
		recordAudit(gov.Decision, gov.DataClasses, 0, 0, 0)
		return "", fmt.Errorf("%s", gov.ErrorMessage)
	}

	var reservationID string
	if opts.Budget != nil {
		estimatedTokens := promptTokens + defaultMaxOutputTokenEstimate
		id, err := opts.Budget.Reserve(ctx, principal.OrgID, principal.UserID, estimatedTokens)
		if err != nil {
			recordAudit(PolicyDenyBudget, gov.DataClasses, 0, 0, 0)
			observability.IncLLMBudgetDenied()
			return "", err
		}
		reservationID = id
	}
	releaseOnFailure := func() {
		if opts.Budget != nil && reservationID != "" {
			opts.Budget.ReleaseReservation(context.Background(), reservationID, principal.OrgID)
		}
	}

	start := time.Now()
	if err := defaultBreaker.Allow(); err != nil {
		recordAudit(gov.Decision, gov.DataClasses, 0, 0, 0)
		releaseOnFailure()
		return "", err
	}
	gen, err := GenerateWithUsage(ctx, client, prompt)
	latency := int(time.Since(start).Milliseconds())
	if err != nil {
		defaultBreaker.RecordFailure()
		recordAudit(gov.Decision, gov.DataClasses, latency, 0, 0)
		releaseOnFailure()
		return "", err
	}
	defaultBreaker.RecordSuccess()
	promptTokens = gen.Usage.PromptTokens
	if promptTokens <= 0 {
		promptTokens = EstimateTokenCount(prompt)
	}
	completionTokens := gen.Usage.CompletionTokens
	if completionTokens <= 0 {
		completionTokens = EstimateTokenCount(gen.Text)
	}
	cost := 0.0
	if opts.Budget != nil {
		cost = opts.Budget.EstimateCostUSD(promptTokens + completionTokens)
		if reservationID != "" {
			opts.Budget.ReconcileUsage(context.Background(), reservationID, principal.OrgID, principal.UserID, promptTokens, completionTokens)
		} else {
			opts.Budget.RecordUsage(ctx, principal.OrgID, principal.UserID, promptTokens, completionTokens)
		}
	}
	observability.IncLLMCall()
	observability.AddLLMTokens(int64(promptTokens + completionTokens))
	recordAudit(gov.Decision, gov.DataClasses, latency, completionTokens, cost)
	return gen.Text, nil
}

func metadata(client Client) (provider, model string) {
	provider = client.Name()
	model = provider
	if modeler, ok := client.(Modeler); ok {
		if m := modeler.Model(); m != "" {
			model = m
		}
	}
	return provider, model
}

// GovernanceFromPrompt builds governance input from prompt options and context flags.
func GovernanceFromPrompt(opts PromptOptions, allowCloud bool, provider string, hasRows, hasRAG bool, sqlText string) GovernanceInput {
	return GovernanceInput{
		Provider:    provider,
		SendRowData: opts.SendRowData,
		AllowCloud:  allowCloud,
		RedactPII:   opts.RedactPII,
		HasRows:     hasRows,
		HasRAG:      hasRAG,
		SQLText:     sqlText,
	}
}
