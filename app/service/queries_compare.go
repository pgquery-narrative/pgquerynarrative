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
	// Result verification actually executes both queries (COUNT(*) + a bounded
	// sample), which is a separate capability from planning them.
	if payload.VerifyResults {
		if err := checkConnectionAccess(ctx, s.authz, connID, auth.ActionQuery); err != nil {
			return nil, connectionForbiddenQueriesError(err)
		}
	}
	runner, err := s.connectionResolver.runnerFor(payload.ConnectionID)
	if err != nil {
		return nil, connectionNotFoundQueriesError(err)
	}

	// A parameterized before/after pair can only be EXPLAIN-ANALYZEd and
	// row-compared once $1/$2/... are bound. Substitute the supplied sample
	// values (AST-verified, then re-validated inside Explain/Run) for the run;
	// the stored candidate keeps its placeholders.
	beforeSQL, afterSQL := payload.BeforeSQL, payload.AfterSQL
	if len(payload.Binds) > 0 {
		if bs, serr := queryrunner.SubstituteParams(beforeSQL, payload.Binds); serr == nil {
			beforeSQL = bs
		} else {
			return nil, &queries.ValidationError{Name: "validation_error", Message: "bind values: " + serr.Error(), Code: strPtr("VALIDATION_ERROR")}
		}
		if as, serr := queryrunner.SubstituteParams(afterSQL, payload.Binds); serr == nil {
			afterSQL = as
		} else {
			return nil, &queries.ValidationError{Name: "validation_error", Message: "bind values: " + serr.Error(), Code: strPtr("VALIDATION_ERROR")}
		}
	}

	beforeResult, err := runner.Explain(ctx, beforeSQL, payload.Analyze)
	if err != nil {
		kind, userMsg := ClassifyRunError(err)
		if kind == RunErrorTimeout {
			return nil, &queries.ValidationError{Name: "timeout_error", Message: userMsg, Code: strPtr("TIMEOUT_ERROR")}
		}
		apilog.ValidationError("compare_plans", "validation_error", err.Error())
		return nil, &queries.ValidationError{Name: "validation_error", Message: userMsg, Code: strPtr("VALIDATION_ERROR")}
	}
	afterResult, err := runner.Explain(ctx, afterSQL, payload.Analyze)
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

	equiv := notRequestedEquivalence()
	if payload.VerifyResults {
		equiv = compareResultEquivalence(ctx, runner, beforeSQL, afterSQL)
	}

	s.persistExplainSnapshot(ctx, ptrString(payload.ConnectionID), payload.Analyze, beforeResult)
	s.persistExplainSnapshot(ctx, ptrString(payload.ConnectionID), payload.Analyze, afterResult)

	out := &queries.ComparePlansResult{
		Before:  beforeAPI,
		After:   afterAPI,
		Metrics: metrics,
		Diff: &queries.PlanComparisonDiff{
			Removed:  cmp.Diff.Removed,
			Added:    cmp.Diff.Added,
			Improved: cmp.Diff.Improved,
		},
		// nil = unverified (run failed / incomplete); never map errors to false/"Different".
		ResultChecksumEqual: equiv.Equal,
	}
	status := equiv.Status
	out.ResultEquivalenceStatus = &status
	notes := equiv.Notes
	out.ResultEquivalenceNotes = &notes
	bc := equiv.BeforeCount
	out.ResultBeforeRowCount = &bc
	ac := equiv.AfterCount
	out.ResultAfterRowCount = &ac
	if equiv.SampleRows > 0 {
		sr := int32(equiv.SampleRows)
		out.ResultSampleRows = &sr
	}
	return out, nil
}

func explainResultToAPI(result *queryrunner.ExplainResult) *queries.ExplainQueryResult {
	findings := make([]*queries.PlanFinding, 0, len(result.Findings))
	for _, f := range result.Findings {
		findings = append(findings, planFindingToAPI(f))
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

// planFindingToAPI maps an internal PlanFinding (including IndexAdvice) to the
// Goa API type. IndexAdvice was previously dropped at this boundary.
func planFindingToAPI(f queryrunner.PlanFinding) *queries.PlanFinding {
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
	if len(f.RelatedColumns) > 0 {
		pf.RelatedColumns = append([]string(nil), f.RelatedColumns...)
	}
	if f.IndexAdvice != nil {
		pf.IndexAdvice = indexAdviceToAPI(f.IndexAdvice)
	}
	return pf
}

func indexAdviceToAPI(a *queryrunner.IndexAdvice) *queries.IndexAdvice {
	if a == nil {
		return nil
	}
	out := &queries.IndexAdvice{}
	if len(a.RelatedColumns) > 0 {
		out.RelatedColumns = append([]string(nil), a.RelatedColumns...)
	}
	if len(a.Issues) > 0 {
		out.Issues = append([]string(nil), a.Issues...)
	}
	if a.PotentialBenefit != "" {
		v := a.PotentialBenefit
		out.PotentialBenefit = &v
	}
	if a.WriteCost != "" {
		v := a.WriteCost
		out.WriteCost = &v
	}
	if a.StorageCost != "" {
		v := a.StorageCost
		out.StorageCost = &v
	}
	if a.CandidateDDL != "" {
		v := a.CandidateDDL
		out.CandidateDdl = &v
	}
	if len(a.RelatedIndexes) > 0 {
		out.RelatedIndexes = make([]*queries.IndexDefinition, 0, len(a.RelatedIndexes))
		for _, idx := range a.RelatedIndexes {
			out.RelatedIndexes = append(out.RelatedIndexes, indexDefinitionToAPI(idx))
		}
	}
	return out
}

func indexDefinitionToAPI(idx queryrunner.IndexDefinition) *queries.IndexDefinition {
	out := &queries.IndexDefinition{
		Name:       idx.Name,
		Definition: idx.Definition,
		IsUnique:   idx.IsUnique,
		IsPrimary:  idx.IsPrimary,
		IsValid:    idx.IsValid,
	}
	if len(idx.KeyColumns) > 0 {
		out.KeyColumns = append([]string(nil), idx.KeyColumns...)
	}
	if len(idx.IncludeColumns) > 0 {
		out.IncludeColumns = append([]string(nil), idx.IncludeColumns...)
	}
	if idx.Predicate != "" {
		v := idx.Predicate
		out.Predicate = &v
	}
	if idx.SizeBytes != 0 {
		v := idx.SizeBytes
		out.SizeBytes = &v
	}
	vScans := idx.IndexScans
	out.IndexScans = &vScans
	vRead := idx.TuplesRead
	out.TuplesRead = &vRead
	vFetched := idx.TuplesFetched
	out.TuplesFetched = &vFetched
	return out
}

// compareResultEquivalence and fingerprint helpers live in equivalence.go.
