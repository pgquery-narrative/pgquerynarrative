package story

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/pgquerynarrative/pgquerynarrative/app/llm"
	"github.com/pgquerynarrative/pgquerynarrative/app/metrics"
)

// Generator creates narratives from query results
type Generator struct {
	llmClient  llm.Client
	promptOpts llm.PromptOptions
	audit      *llm.AuditStore
	budget     *llm.BudgetStore
	allowCloud bool
}

// NewGenerator creates a new narrative generator
func NewGenerator(llmClient llm.Client) *Generator {
	return &Generator{
		llmClient:  llmClient,
		promptOpts: llm.DefaultPromptOptions(),
	}
}

// SetPromptOptions configures LLM data governance for narrative prompts.
func (g *Generator) SetPromptOptions(opts llm.PromptOptions) {
	g.promptOpts = opts
}

// PromptOptions returns configured narrative prompt options.
func (g *Generator) PromptOptions() llm.PromptOptions {
	return g.promptOpts
}

// SetGovernance configures audit logging, budgets, and cloud-data policy for narrative generation.
func (g *Generator) SetGovernance(audit *llm.AuditStore, budget *llm.BudgetStore, allowCloud bool) {
	g.audit = audit
	g.budget = budget
	g.allowCloud = allowCloud
}

// Generate creates a narrative from query results and metrics. similarQueriesContext
// is optional RAG context (e.g. similar past query names and SQL snippets) to improve the narrative.
func (g *Generator) Generate(ctx context.Context, sql string, columns []string, rows [][]interface{}, calcMetrics *metrics.Metrics, similarQueriesContext string) (*NarrativeContent, error) {
	metricsJSON, err := json.Marshal(calcMetrics)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal metrics: %w", err)
	}

	hasPeriodComparison := false
	for _, ts := range calcMetrics.TimeSeries {
		if ts.PreviousPeriod != nil {
			hasPeriodComparison = true
			break
		}
	}

	prompt := llm.BuildNarrativePrompt(sql, columns, rows, string(metricsJSON), hasPeriodComparison, similarQueriesContext, g.promptOpts)

	hasRows := len(rows) > 0 && g.promptOpts.SendRowData
	gov := llm.GovernanceFromPrompt(g.promptOpts, g.allowCloud, g.llmClient.Name(), hasRows, similarQueriesContext != "", sql)
	response, err := llm.InvokeWithBudget(ctx, g.llmClient, llm.InvokeOptions{Audit: g.audit, Budget: g.budget}, "narrative_generate", gov, prompt)
	if err != nil {
		return nil, fmt.Errorf("failed to generate narrative: %w", err)
	}

	narrative, err := ParseNarrative(response, string(metricsJSON))
	if err != nil {
		return nil, fmt.Errorf("failed to parse narrative: %w", err)
	}

	if !hasPeriodComparison {
		RemoveFabricatedPeriodComparison(narrative)
	}

	return narrative, nil
}
