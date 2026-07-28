package service

import (
	"context"
	"encoding/json"

	"github.com/pgquerynarrative/pgquerynarrative/api/gen/queries"
	"github.com/pgquerynarrative/pgquerynarrative/app/apilog"
	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/app/queryrunner"
)

// ComparePlans runs EXPLAIN on before and after SQL and returns a structured comparison.
func (s *QueriesService) ComparePlans(ctx context.Context, payload *queries.ComparePlansPayload) (*queries.ComparePlansResult, error) {
	connID, err := s.connectionResolver.resolveConnectionID(payload.ConnectionID)
	if err != nil {
		return nil, connectionNotFoundQueriesError(err)
	}
	explainAction := auth.ActionExplain
	if payload.Analyze {
		explainAction = auth.ActionAnalyze
	}
	if err := checkConnectionAccess(ctx, s.authz, connID, explainAction); err != nil {
		return nil, connectionForbiddenQueriesError(err)
	}
	runner, err := s.connectionResolver.runnerFor(payload.ConnectionID)
	if err != nil {
		return nil, connectionNotFoundQueriesError(err)
	}

	beforeResult, err := runner.Explain(ctx, payload.BeforeSQL, payload.Analyze)
	if err != nil {
		kind, userMsg := ClassifyRunError(err)
		if kind == RunErrorTimeout {
			return nil, &queries.ValidationError{Name: "timeout_error", Message: userMsg, Code: strPtr("TIMEOUT_ERROR")}
		}
		apilog.ValidationError("compare_plans", "validation_error", err.Error())
		return nil, &queries.ValidationError{Name: "validation_error", Message: userMsg, Code: strPtr("VALIDATION_ERROR")}
	}
	afterResult, err := runner.Explain(ctx, payload.AfterSQL, payload.Analyze)
	if err != nil {
		kind, userMsg := ClassifyRunError(err)
		if kind == RunErrorTimeout {
			return nil, &queries.ValidationError{Name: "timeout_error", Message: userMsg, Code: strPtr("TIMEOUT_ERROR")}
		}
		apilog.ValidationError("compare_plans", "validation_error", err.Error())
		return nil, &queries.ValidationError{Name: "validation_error", Message: userMsg, Code: strPtr("VALIDATION_ERROR")}
	}

	cmp, err := queryrunner.ComparePlans(beforeResult.Plan, afterResult.Plan)
	if err != nil {
		apilog.ValidationError("compare_plans", "validation_error", err.Error())
		return nil, &queries.ValidationError{Name: "validation_error", Message: "failed to compare plans", Code: strPtr("VALIDATION_ERROR")}
	}

	beforeAPI := explainResultToAPI(beforeResult)
	afterAPI := explainResultToAPI(afterResult)

	metrics := make([]*queries.PlanComparisonMetric, 0, len(cmp.Metrics))
	for _, m := range cmp.Metrics {
		metrics = append(metrics, &queries.PlanComparisonMetric{
			Evidence: m.Evidence,
			Before:   m.Before,
			After:    m.After,
			Change:   m.Change,
		})
	}

	checksumEqual := compareResultChecksums(ctx, runner, payload.BeforeSQL, payload.AfterSQL)

	s.persistExplainSnapshot(ctx, ptrString(payload.ConnectionID), payload.Analyze, beforeResult)
	s.persistExplainSnapshot(ctx, ptrString(payload.ConnectionID), payload.Analyze, afterResult)

	return &queries.ComparePlansResult{
		Before:  beforeAPI,
		After:   afterAPI,
		Metrics: metrics,
		Diff: &queries.PlanComparisonDiff{
			Removed:  cmp.Diff.Removed,
			Added:    cmp.Diff.Added,
			Improved: cmp.Diff.Improved,
		},
		ResultChecksumEqual: &checksumEqual,
	}, nil
}

func explainResultToAPI(result *queryrunner.ExplainResult) *queries.ExplainQueryResult {
	findings := make([]*queries.PlanFinding, 0, len(result.Findings))
	for _, f := range result.Findings {
		cost := f.EstimatedCost
		pf := &queries.PlanFinding{
			NodeType:      f.NodeType,
			EstimatedCost: &cost,
			IsSeqScan:     f.IsSeqScan,
			Message:       f.Message,
		}
		if f.Schema != "" {
			pf.Schema = &f.Schema
		}
		if f.Relation != "" {
			pf.Relation = &f.Relation
		}
		if f.Category != "" {
			pf.Category = &f.Category
		}
		if f.Confidence != "" {
			pf.Confidence = &f.Confidence
		}
		if len(f.Evidence) > 0 {
			pf.Evidence = f.Evidence
		}
		findings = append(findings, pf)
	}
	var plan any
	if len(result.Plan) > 0 {
		_ = json.Unmarshal(result.Plan, &plan)
	}
	return &queries.ExplainQueryResult{
		SQL:             result.SQL,
		TotalCost:       result.TotalCost,
		Plan:            plan,
		Findings:        findings,
		ExecutionTimeMs: result.ExecutionTimeMs,
	}
}

func compareResultChecksums(ctx context.Context, runner *queryrunner.Runner, beforeSQL, afterSQL string) bool {
	before, err := runner.Run(ctx, beforeSQL, 1000)
	if err != nil {
		return false
	}
	after, err := runner.Run(ctx, afterSQL, 1000)
	if err != nil {
		return false
	}
	return hashResult(before) == hashResult(after)
}

func hashResult(result *queryrunner.Result) string {
	if result == nil {
		return ""
	}
	data, err := json.Marshal(result.Rows)
	if err != nil {
		return ""
	}
	return string(data)
}
