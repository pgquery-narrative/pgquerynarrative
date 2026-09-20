package queryrunner

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PlanAnalysis is what the engine concludes from one EXPLAIN (FORMAT JSON) document.
type PlanAnalysis struct {
	TotalCost float64
	Plan      json.RawMessage
	Metrics   PlanMetrics
	Findings  []PlanFinding
	Diagnosis *Diagnosis
}

// AnalyzePlanJSON runs the plan-finding rules over a plan that came from anywhere, for example
// from pqn_api.plan, without a Runner, a validator or a tenant. When pool is not nil the findings
// are enriched from the catalogs (table size, existing indexes, statistics freshness), which is
// what turns "sequential scan" into index advice. The pool needs only catalog read access.
func AnalyzePlanJSON(ctx context.Context, pool *pgxpool.Pool, planJSON []byte) (*PlanAnalysis, error) {
	parsed, err := parseExplainJSON(planJSON)
	if err != nil {
		return nil, fmt.Errorf("analyze plan: %w", err)
	}
	metrics, err := MetricsFromPlan(parsed.PlanJSON)
	if err != nil {
		return nil, fmt.Errorf("analyze plan: %w", err)
	}
	findings := parsed.Findings
	if pool != nil {
		findings = (&Runner{pool: pool}).enrichExplainFindings(ctx, findings)
	}
	return &PlanAnalysis{
		TotalCost: parsed.TotalCost,
		Plan:      parsed.PlanJSON,
		Metrics:   metrics,
		Findings:  findings,
		Diagnosis: Diagnose(findings, metrics),
	}, nil
}
