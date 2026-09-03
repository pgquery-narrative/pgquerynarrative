package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pgquerynarrative/pgquerynarrative/api/gen/investigations"
	"github.com/pgquerynarrative/pgquerynarrative/api/gen/queries"
	"github.com/pgquerynarrative/pgquerynarrative/api/gen/reports"
	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/app/db"
	"github.com/pgquerynarrative/pgquerynarrative/app/queryrunner"
	"github.com/pgquerynarrative/pgquerynarrative/app/story"
)

// InvestigationsService handles Query Investigation workflows.
type InvestigationsService struct {
	appPool    db.DB
	queriesSvc *QueriesService
	reportsSvc *ReportsService
}

// NewInvestigationsService creates an investigations service.
func NewInvestigationsService(appPool db.DB, queriesSvc *QueriesService, reportsSvc *ReportsService) *InvestigationsService {
	return &InvestigationsService{appPool: appPool, queriesSvc: queriesSvc, reportsSvc: reportsSvc}
}

// Create starts a new investigation with automatic EXPLAIN evidence.
func (s *InvestigationsService) Create(ctx context.Context, payload *investigations.CreateInvestigationPayload) (*investigations.Investigation, error) {
	p := auth.PrincipalFromContext(ctx)
	connID := "default"
	if payload.ConnectionID != nil && *payload.ConnectionID != "" {
		connID = *payload.ConnectionID
	}

	explainResult, err := s.queriesSvc.ExplainPlan(ctx, &queries.ExplainQueryPayload{
		SQL:          payload.SQL,
		Analyze:      true,
		ConnectionID: &connID,
	})
	if err != nil {
		// Fall back to estimate-only when EXPLAIN ANALYZE is disabled server-side.
		explainResult, err = s.queriesSvc.ExplainPlan(ctx, &queries.ExplainQueryPayload{
			SQL:          payload.SQL,
			Analyze:      false,
			ConnectionID: &connID,
		})
	}
	if err != nil {
		return nil, normalizeInvestigationError(err)
	}

	var statSnap *investigations.StatSnapshot
	if payload.Calls != nil || payload.MeanTimeMs != nil {
		statSnap = &investigations.StatSnapshot{}
		if payload.Queryid != nil {
			statSnap.Queryid = payload.Queryid
		}
		if payload.Calls != nil {
			statSnap.Calls = payload.Calls
		}
		if payload.MeanTimeMs != nil {
			statSnap.MeanTimeMs = payload.MeanTimeMs
		}
		if payload.TotalTimeMs != nil {
			statSnap.TotalTimeMs = payload.TotalTimeMs
		}
		if payload.Rows != nil {
			statSnap.Rows = payload.Rows
		}
	}

	explainJSON, _ := json.Marshal(explainResult)
	statJSON, _ := json.Marshal(statSnap)
	fingerprint := sqlFingerprint(payload.SQL)

	var id string
	err = s.appPool.QueryRow(ctx, `
		INSERT INTO app.investigations (
			organization_id, created_by, title, status, sql, connection_id,
			query_fingerprint, stat_snapshot, explain_result
		) VALUES ($1, $2, $3, 'analyzing', $4, $5, $6, $7, $8)
		RETURNING id
	`, p.OrgID, p.UserID, payload.Title, payload.SQL, connID, fingerprint, statJSON, explainJSON).Scan(&id)
	if err != nil {
		return nil, err
	}

	// Mark as open after explain completes.
	_, _ = s.appPool.Exec(ctx, `UPDATE app.investigations SET status = 'open', updated_at = now() WHERE id = $1`, id)

	return s.Get(ctx, &investigations.GetPayload{ID: id})
}

// CreateFromRegression opens an investigation from a regression alert: loads full
// SQL (preferring the latest snapshot when the alert text was truncated), runs
// EXPLAIN via Create, and links the alert to the new investigation.
func (s *InvestigationsService) CreateFromRegression(ctx context.Context, payload *investigations.CreateFromRegressionPayload) (*investigations.Investigation, error) {
	org := orgID(ctx)
	row := s.appPool.QueryRow(ctx, `
		SELECT id, title, query_text, COALESCE(connection_id, 'default'),
		       queryid, investigation_id,
		       calls, mean_time_ms, total_time_ms, rows_count
		FROM app.regression_alerts
		WHERE id = $1 AND organization_id = $2
	`, payload.RegressionAlertID, org)

	var alertID, title, queryText, connID string
	var queryid, existingInv *string
	var calls, rowsCount *int64
	var meanMs, totalMs *float64
	if err := row.Scan(&alertID, &title, &queryText, &connID, &queryid, &existingInv, &calls, &meanMs, &totalMs, &rowsCount); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, &investigations.NotFoundError{Name: "not_found", Message: "regression alert not found", Code: strPtr("NOT_FOUND")}
		}
		return nil, err
	}
	if existingInv != nil && *existingInv != "" {
		return s.Get(ctx, &investigations.GetPayload{ID: *existingInv})
	}

	sqlText := strings.TrimSpace(queryText)
	if queryid != nil && *queryid != "" {
		if better := s.latestSnapshotSQL(ctx, org, *queryid); better != "" && len(better) >= len(sqlText) {
			sqlText = better
		}
	}
	if sqlText == "" || strings.HasSuffix(sqlText, "...") {
		return nil, &investigations.ValidationError{
			Name:    "validation_error",
			Message: "regression alert has incomplete SQL; open from Query Stats with a full statement, or wait for a fresh poller snapshot",
			Code:    strPtr("VALIDATION_ERROR"),
		}
	}
	// CreateInvestigation rejects semicolons.
	sqlText = strings.TrimSuffix(strings.TrimSpace(sqlText), ";")

	createPayload := &investigations.CreateInvestigationPayload{
		Title:        title,
		SQL:          sqlText,
		ConnectionID: &connID,
		Queryid:      queryid,
		Calls:        calls,
		MeanTimeMs:   meanMs,
		TotalTimeMs:  totalMs,
		Rows:         rowsCount,
	}
	inv, err := s.Create(ctx, createPayload)
	if err != nil {
		return nil, err
	}
	_, _ = s.appPool.Exec(ctx, `
		UPDATE app.regression_alerts
		SET investigation_id = $1
		WHERE id = $2 AND organization_id = $3 AND investigation_id IS NULL
	`, inv.ID, alertID, org)
	return inv, nil
}

func (s *InvestigationsService) latestSnapshotSQL(ctx context.Context, orgID, queryID string) string {
	var sqlText string
	err := s.appPool.QueryRow(ctx, `
		SELECT s.query_text
		FROM app.stat_statement_snapshots s
		JOIN app.stat_statement_polls p ON p.id = s.poll_id
		WHERE p.organization_id = $1 AND s.queryid = $2
		ORDER BY p.captured_at DESC
		LIMIT 1
	`, orgID, queryID).Scan(&sqlText)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(sqlText)
}

// List returns investigations for the current organization.
func (s *InvestigationsService) List(ctx context.Context, payload *investigations.ListPayload) (*investigations.InvestigationList, error) {
	limit := int(payload.Limit)
	if limit == 0 {
		limit = 20
	}
	offset := int(payload.Offset)

	rows, err := s.appPool.Query(ctx, `
		SELECT id FROM app.investigations
		WHERE organization_id = $1
		ORDER BY updated_at DESC
		LIMIT $2 OFFSET $3
	`, orgID(ctx), limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := &investigations.InvestigationList{
		Items:  []*investigations.Investigation{},
		Limit:  int32(limit),
		Offset: int32(offset),
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		item, err := s.Get(ctx, &investigations.GetPayload{ID: id})
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, item)
	}
	return out, rows.Err()
}

// Get returns one investigation by ID.
func (s *InvestigationsService) Get(ctx context.Context, payload *investigations.GetPayload) (*investigations.Investigation, error) {
	row := s.appPool.QueryRow(ctx, `
		SELECT id, title, status, sql, connection_id, query_fingerprint,
		       stat_snapshot, explain_result, candidate_sql, candidate_explain,
		       comparison, report_id, created_at, updated_at,
		       fix_status, fix_reference, fix_applied_at,
		       fix_baseline_mean_ms, fix_confirmed_mean_ms, fix_measured_at
		FROM app.investigations
		WHERE id = $1 AND organization_id = $2
	`, payload.ID, orgID(ctx))

	var inv investigations.Investigation
	var statJSON, explainJSON, candidateExplainJSON, comparisonJSON []byte
	var candidateSQL, reportID, fingerprint, fixReference *string
	var createdAt, updatedAt time.Time
	var fixStatus string
	var fixAppliedAt, fixMeasuredAt *time.Time
	var fixBaselineMean, fixConfirmedMean *float64

	err := row.Scan(
		&inv.ID, &inv.Title, &inv.Status, &inv.SQL, &inv.ConnectionID, &fingerprint,
		&statJSON, &explainJSON, &candidateSQL, &candidateExplainJSON,
		&comparisonJSON, &reportID, &createdAt, &updatedAt,
		&fixStatus, &fixReference, &fixAppliedAt,
		&fixBaselineMean, &fixConfirmedMean, &fixMeasuredAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, &investigations.NotFoundError{Name: "not_found", Message: "investigation not found", Code: strPtr("NOT_FOUND")}
		}
		return nil, err
	}
	inv.CreatedAt = createdAt.Format(time.RFC3339)
	inv.UpdatedAt = updatedAt.Format(time.RFC3339)
	if fingerprint != nil {
		inv.QueryFingerprint = fingerprint
	}
	if candidateSQL != nil {
		inv.CandidateSQL = candidateSQL
	}
	if reportID != nil {
		inv.ReportID = reportID
	}
	if len(statJSON) > 0 && string(statJSON) != "null" {
		var snap investigations.StatSnapshot
		if json.Unmarshal(statJSON, &snap) == nil {
			inv.StatSnapshot = &snap
		}
	}
	if len(explainJSON) > 0 {
		var exp investigations.ExplainQueryResult
		if json.Unmarshal(explainJSON, &exp) == nil {
			attachDiagnosis(&exp)
			inv.Explain = &exp
		}
	}
	if len(candidateExplainJSON) > 0 {
		var exp investigations.ExplainQueryResult
		if json.Unmarshal(candidateExplainJSON, &exp) == nil {
			inv.CandidateExplain = &exp
		}
	}
	if len(comparisonJSON) > 0 {
		var cmp investigations.ComparePlansResult
		if json.Unmarshal(comparisonJSON, &cmp) == nil {
			inv.Comparison = &cmp
		}
	}
	if fixStatus != "" {
		fs := fixStatus
		inv.FixStatus = &fs
	}
	inv.FixReference = fixReference
	if fixAppliedAt != nil {
		v := fixAppliedAt.Format(time.RFC3339)
		inv.FixAppliedAt = &v
	}
	if fixMeasuredAt != nil {
		v := fixMeasuredAt.Format(time.RFC3339)
		inv.FixMeasuredAt = &v
	}
	inv.FixBaselineMeanMs = fixBaselineMean
	inv.FixConfirmedMeanMs = fixConfirmedMean
	inv.Candidates = s.loadInvestigationCandidates(ctx, inv.ID, inv.CandidateSQL)
	return &inv, nil
}

// fixTransitions is the allowed fix_status graph. "abandoned" is reachable from
// any non-terminal state; "confirmed"/"regressed" are set by the poller, not here.
var fixTransitions = map[string][]string{
	"proposed":  {"verified", "applied", "abandoned"},
	"verified":  {"applied", "proposed", "abandoned"},
	"applied":   {"confirmed", "regressed", "verified", "abandoned"},
	"regressed": {"applied", "abandoned"},
	"confirmed": {"applied"},
	"abandoned": {"proposed"},
}

// fixTransitionAllowed reports whether the fix_status graph permits moving from
// -> to. A no-op (from == to) is the caller's concern, not this function's.
func fixTransitionAllowed(from, to string) bool {
	for _, t := range fixTransitions[from] {
		if t == to {
			return true
		}
	}
	return false
}

// UpdateFix advances an investigation's fix lifecycle and records the PR/ticket
// reference. Marking "applied" snapshots the linked query's current mean latency
// so the poller can later re-measure and confirm the fix.
func (s *InvestigationsService) UpdateFix(ctx context.Context, payload *investigations.UpdateFixPayload) (*investigations.Investigation, error) {
	inv, err := s.Get(ctx, &investigations.GetPayload{ID: payload.ID})
	if err != nil {
		return nil, err
	}
	from := "proposed"
	if inv.FixStatus != nil && *inv.FixStatus != "" {
		from = *inv.FixStatus
	}
	to := from
	if payload.FixStatus != nil && *payload.FixStatus != "" {
		to = *payload.FixStatus
	}
	if to != from {
		if !fixTransitionAllowed(from, to) {
			return nil, &investigations.ValidationError{
				Name:    "validation_error",
				Message: fmt.Sprintf("cannot move fix status from %q to %q", from, to),
				Code:    strPtr("VALIDATION_ERROR"),
			}
		}
	}

	setApplied := to == "applied" && from != "applied"
	_, err = s.appPool.Exec(ctx, `
		UPDATE app.investigations SET
			fix_status = $3,
			fix_reference = COALESCE($4, fix_reference),
			fix_applied_at = CASE WHEN $5 THEN now() ELSE fix_applied_at END,
			fix_baseline_mean_ms = CASE
				WHEN $5 THEN COALESCE(
					(SELECT s.mean_time_ms
					 FROM app.stat_statement_snapshots s
					 JOIN app.stat_statement_polls p ON p.id = s.poll_id
					 JOIN app.regression_alerts ra ON ra.queryid = s.queryid
					   AND ra.investigation_id = app.investigations.id
					 WHERE p.organization_id = $2
					 ORDER BY p.captured_at DESC LIMIT 1),
					(stat_snapshot->>'mean_time_ms')::float,
					fix_baseline_mean_ms)
				ELSE fix_baseline_mean_ms END,
			updated_at = now()
		WHERE id = $1 AND organization_id = $2
	`, payload.ID, orgID(ctx), to, payload.FixReference, setApplied)
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, &investigations.GetPayload{ID: payload.ID})
}

// SuggestRewrite proposes AST-based candidate rewrites from the investigation
// SQL and stored plan findings. Does not require demo scenarios or a pasted rewrite.
func (s *InvestigationsService) SuggestRewrite(ctx context.Context, payload *investigations.SuggestRewritePayload) (*investigations.RewriteSuggestionList, error) {
	inv, err := s.Get(ctx, &investigations.GetPayload{ID: payload.ID})
	if err != nil {
		return nil, err
	}

	findings := queryrunnerFindingsFromInvestigation(inv.Explain)
	cands := queryrunner.SuggestRewrites(inv.SQL, findings)
	out := &investigations.RewriteSuggestionList{
		Candidates: make([]*investigations.RewriteCandidate, 0, len(cands)),
	}
	for _, c := range cands {
		cand := &investigations.RewriteCandidate{
			SQL:       c.SQL,
			Rationale: c.Rationale,
		}
		if c.Category != "" {
			cat := c.Category
			cand.Category = &cat
		}
		if c.Confidence != "" {
			conf := c.Confidence
			cand.Confidence = &conf
		}
		out.Candidates = append(out.Candidates, cand)
	}
	return out, nil
}

// attachDiagnosis derives the verdict-first rollup from the stored findings and
// plan and sets exp.Diagnosis. Computed at read time so it always reflects the
// current diagnosis logic and needs no backfill of historical rows.
func attachDiagnosis(exp *investigations.ExplainQueryResult) {
	if exp == nil || len(exp.Findings) == 0 {
		return
	}
	findings := queryrunnerFindingsFromInvestigation(exp)

	var metrics queryrunner.PlanMetrics
	if planBytes, err := json.Marshal(exp.Plan); err == nil {
		if !isExplainRootArray(planBytes) {
			if wrapped, werr := json.Marshal([]map[string]any{{"Plan": exp.Plan}}); werr == nil {
				planBytes = wrapped
			}
		}
		if m, merr := queryrunner.MetricsFromPlan(planBytes); merr == nil {
			metrics = m
		}
	}
	if metrics.TotalCost == 0 {
		metrics.TotalCost = exp.TotalCost
	}

	d := queryrunner.Diagnose(findings, metrics)
	if d == nil {
		return
	}
	exp.Diagnosis = planDiagnosisToAPI(d)
}

func planDiagnosisToAPI(d *queryrunner.Diagnosis) *investigations.PlanDiagnosis {
	out := &investigations.PlanDiagnosis{RawCount: d.RawCount}
	if d.Headline != "" {
		out.Headline = &d.Headline
	}
	if d.Summary != "" {
		out.Summary = &d.Summary
	}
	if d.RootCause != nil {
		out.RootCause = planCauseToAPI(*d.RootCause)
	}
	for _, c := range d.Causes {
		out.Causes = append(out.Causes, planCauseToAPI(c))
	}
	if d.Incidental != nil {
		out.Incidental = &investigations.PlanDiagnosisIncidental{
			Count:      d.Incidental.Count,
			Summary:    d.Incidental.Summary,
			Categories: d.Incidental.Categories,
		}
	}
	return out
}

func planCauseToAPI(c queryrunner.DiagnosisCause) *investigations.PlanDiagnosisCause {
	out := &investigations.PlanDiagnosisCause{
		Category:  c.Category,
		Title:     c.Title,
		Severity:  string(c.Severity),
		NodeTypes: c.NodeTypes,
		Evidence:  c.Evidence,
	}
	if c.Detail != "" {
		out.Detail = &c.Detail
	}
	if c.Fix != "" {
		out.Fix = &c.Fix
	}
	if c.CostShare > 0 {
		cs := c.CostShare
		out.CostShare = &cs
	}
	if c.Occurrences > 0 {
		occ := c.Occurrences
		out.Occurrences = &occ
	}
	return out
}

func queryrunnerFindingsFromInvestigation(exp *investigations.ExplainQueryResult) []queryrunner.PlanFinding {
	if exp == nil {
		return nil
	}
	out := make([]queryrunner.PlanFinding, 0, len(exp.Findings))
	for _, f := range exp.Findings {
		if f == nil {
			continue
		}
		pf := queryrunner.PlanFinding{
			NodeType:  f.NodeType,
			IsSeqScan: f.IsSeqScan,
			Message:   f.Message,
			Evidence:  f.Evidence,
		}
		if f.Category != nil {
			pf.Category = *f.Category
		}
		if f.Confidence != nil {
			pf.Confidence = *f.Confidence
		}
		if f.Schema != nil {
			pf.Schema = *f.Schema
		}
		if f.Relation != nil {
			pf.Relation = *f.Relation
		}
		if f.EstimatedCost != nil {
			pf.EstimatedCost = *f.EstimatedCost
		}
		if len(f.RelatedColumns) > 0 {
			pf.RelatedColumns = append([]string(nil), f.RelatedColumns...)
		}
		if f.IndexAdvice != nil {
			pf.IndexAdvice = indexAdviceFromAPI(f.IndexAdvice)
		}
		out = append(out, pf)
	}
	return out
}

func indexAdviceFromAPI(a *investigations.IndexAdvice) *queryrunner.IndexAdvice {
	if a == nil {
		return nil
	}
	out := &queryrunner.IndexAdvice{
		RelatedColumns: append([]string(nil), a.RelatedColumns...),
		Issues:         append([]string(nil), a.Issues...),
	}
	if a.PotentialBenefit != nil {
		out.PotentialBenefit = *a.PotentialBenefit
	}
	if a.WriteCost != nil {
		out.WriteCost = *a.WriteCost
	}
	if a.StorageCost != nil {
		out.StorageCost = *a.StorageCost
	}
	if a.CandidateDdl != nil {
		out.CandidateDDL = *a.CandidateDdl
	}
	return out
}

// RankCandidates generates rewrite + index-DDL candidates, dry-EXPLAINs rewrites,
// and ranks them by cost / partitions (and timing when analyze is requested).
func (s *InvestigationsService) RankCandidates(ctx context.Context, payload *investigations.RankCandidatesPayload) (*investigations.RankedCandidateList, error) {
	inv, err := s.Get(ctx, &investigations.GetPayload{ID: payload.ID})
	if err != nil {
		return nil, err
	}

	analyze := payload.Analyze
	baselineAPI, err := s.queriesSvc.ExplainPlan(ctx, &queries.ExplainQueryPayload{
		SQL:          inv.SQL,
		Analyze:      analyze,
		ConnectionID: &inv.ConnectionID,
	})
	if err != nil && analyze {
		baselineAPI, err = s.queriesSvc.ExplainPlan(ctx, &queries.ExplainQueryPayload{
			SQL:          inv.SQL,
			Analyze:      false,
			ConnectionID: &inv.ConnectionID,
		})
		analyze = false
	}
	if err != nil {
		return nil, normalizeInvestigationError(err)
	}

	baselineMetrics, err := metricsFromExplainAPI(baselineAPI)
	if err != nil {
		return nil, &investigations.ValidationError{Name: "validation_error", Message: "could not read baseline plan metrics", Code: strPtr("VALIDATION_ERROR")}
	}

	// Prefer freshly explained findings (includes current IndexAdvice / DDL).
	// Stored investigation JSON may predate IndexAdvice shipping or lack catalog enrichment.
	findings := queryrunnerFindingsFromQueriesAPI(baselineAPI)
	if len(findings) == 0 {
		findings = queryrunnerFindingsFromInvestigation(inv.Explain)
	}

	var scored []queryrunner.ScoredCandidate
	for _, rewrite := range queryrunner.SuggestRewrites(inv.SQL, findings) {
		afterAPI, explErr := s.queriesSvc.ExplainPlan(ctx, &queries.ExplainQueryPayload{
			SQL:          rewrite.SQL,
			Analyze:      analyze,
			ConnectionID: &inv.ConnectionID,
		})
		if explErr != nil {
			continue
		}
		afterMetrics, mErr := metricsFromExplainAPI(afterAPI)
		if mErr != nil {
			continue
		}
		improved := []string{}
		if beforePlan, afterPlan, ok := planBytesPair(baselineAPI, afterAPI); ok {
			if cmp, cErr := queryrunner.ComparePlans(beforePlan, afterPlan); cErr == nil && cmp != nil {
				improved = cmp.Diff.Improved
			}
		}
		scored = append(scored, queryrunner.ScoreSQLRewrite(rewrite, baselineMetrics, afterMetrics, improved))
	}
	for _, idxCand := range queryrunner.CollectIndexDDLCandidates(findings) {
		proj := s.queriesSvc.ProjectIndexCost(ctx, &inv.ConnectionID, inv.SQL, idxCand.DDL, baselineMetrics.TotalCost)
		scored = append(scored, queryrunner.ScoreIndexProjection(idxCand, proj))
	}
	scored = queryrunner.RankScoredCandidates(scored)

	out := &investigations.RankedCandidateList{
		Candidates: make([]*investigations.RankedCandidate, 0, len(scored)),
	}
	base := &investigations.RankedCandidateBaseline{
		TotalCost: &baselineMetrics.TotalCost,
	}
	parts := queryrunnerPartitionCount(baselineMetrics)
	base.PartitionsScanned = &parts
	if baselineMetrics.HasActualTiming {
		t := baselineMetrics.ExecutionTimeMs
		base.ExecutionTimeMs = &t
	}
	out.Baseline = base

	for _, c := range scored {
		out.Candidates = append(out.Candidates, scoredCandidateToAPI(c))
	}
	return out, nil
}

func scoredCandidateToAPI(c queryrunner.ScoredCandidate) *investigations.RankedCandidate {
	out := &investigations.RankedCandidate{
		Kind:      c.Kind,
		Rankable:  c.Rankable,
		Rationale: c.Rationale,
	}
	if c.Rank > 0 {
		r := int32(c.Rank)
		out.Rank = &r
	}
	if c.SQL != "" {
		out.SQL = &c.SQL
	}
	if c.DDL != "" {
		out.Ddl = &c.DDL
	}
	if c.Category != "" {
		out.Category = &c.Category
	}
	if c.Confidence != "" {
		out.Confidence = &c.Confidence
	}
	if c.Rankable {
		tc, cd := c.TotalCost, c.CostDelta
		ps, pd := c.PartitionsScanned, c.PartitionsDelta
		out.TotalCost = &tc
		out.CostDelta = &cd
		out.PartitionsScanned = &ps
		out.PartitionsDelta = &pd
		if c.HasTiming {
			t := c.ExecutionTimeMs
			out.ExecutionTimeMs = &t
		}
	}
	if len(c.Improved) > 0 {
		out.Improved = append([]string(nil), c.Improved...)
	}
	if c.ProjectionMethod != "" {
		m := c.ProjectionMethod
		out.ProjectionMethod = &m
	}
	return out
}

func metricsFromExplainAPI(exp *queries.ExplainQueryResult) (queryrunner.PlanMetrics, error) {
	if exp == nil {
		return queryrunner.PlanMetrics{}, errNoPlan
	}
	planBytes, err := json.Marshal(exp.Plan)
	if err != nil {
		return queryrunner.PlanMetrics{}, err
	}
	// Explain API returns the inner plan object; MetricsFromPlan expects the
	// top-level EXPLAIN JSON array wrapping {"Plan": ...}.
	if !isExplainRootArray(planBytes) {
		wrapped, _ := json.Marshal([]map[string]any{{"Plan": exp.Plan}})
		planBytes = wrapped
	}
	m, err := queryrunner.MetricsFromPlan(planBytes)
	if err != nil {
		return queryrunner.PlanMetrics{}, err
	}
	if m.TotalCost == 0 && exp.TotalCost > 0 {
		m.TotalCost = exp.TotalCost
	}
	// Only fold in the API's timing when it is an OBSERVED server execution time
	// (ANALYZE), never the request wall clock. MetricsFromPlan already reads
	// "Actual Total Time" from the same plan, so this is a belt-and-suspenders
	// fallback for a trimmed plan payload.
	if exp.EvidenceMode == queryrunner.EvidenceObserved && exp.ServerExecutionTimeMs != nil &&
		*exp.ServerExecutionTimeMs > 0 && !m.HasActualTiming {
		m.ExecutionTimeMs = *exp.ServerExecutionTimeMs
		m.HasActualTiming = true
	}
	return m, nil
}

var errNoPlan = errors.New("no plan")

func isExplainRootArray(b []byte) bool {
	for _, c := range b {
		if c == ' ' || c == '\n' || c == '\t' || c == '\r' {
			continue
		}
		return c == '['
	}
	return false
}

func planBytesPair(before, after *queries.ExplainQueryResult) (json.RawMessage, json.RawMessage, bool) {
	if before == nil || after == nil {
		return nil, nil, false
	}
	bb, err1 := json.Marshal([]map[string]any{{"Plan": before.Plan}})
	ab, err2 := json.Marshal([]map[string]any{{"Plan": after.Plan}})
	if err1 != nil || err2 != nil {
		return nil, nil, false
	}
	return bb, ab, true
}

func queryrunnerFindingsFromQueriesAPI(exp *queries.ExplainQueryResult) []queryrunner.PlanFinding {
	if exp == nil {
		return nil
	}
	out := make([]queryrunner.PlanFinding, 0, len(exp.Findings))
	for _, f := range exp.Findings {
		if f == nil {
			continue
		}
		pf := queryrunner.PlanFinding{
			NodeType:       f.NodeType,
			IsSeqScan:      f.IsSeqScan,
			Message:        f.Message,
			Evidence:       f.Evidence,
			RelatedColumns: append([]string(nil), f.RelatedColumns...),
		}
		if f.Category != nil {
			pf.Category = *f.Category
		}
		if f.Confidence != nil {
			pf.Confidence = *f.Confidence
		}
		if f.Schema != nil {
			pf.Schema = *f.Schema
		}
		if f.Relation != nil {
			pf.Relation = *f.Relation
		}
		if f.EstimatedCost != nil {
			pf.EstimatedCost = *f.EstimatedCost
		}
		if f.IndexAdvice != nil {
			pf.IndexAdvice = &queryrunner.IndexAdvice{
				RelatedColumns: append([]string(nil), f.IndexAdvice.RelatedColumns...),
				Issues:         append([]string(nil), f.IndexAdvice.Issues...),
			}
			if f.IndexAdvice.PotentialBenefit != nil {
				pf.IndexAdvice.PotentialBenefit = *f.IndexAdvice.PotentialBenefit
			}
			if f.IndexAdvice.WriteCost != nil {
				pf.IndexAdvice.WriteCost = *f.IndexAdvice.WriteCost
			}
			if f.IndexAdvice.StorageCost != nil {
				pf.IndexAdvice.StorageCost = *f.IndexAdvice.StorageCost
			}
			if f.IndexAdvice.CandidateDdl != nil {
				pf.IndexAdvice.CandidateDDL = *f.IndexAdvice.CandidateDdl
			}
		}
		out = append(out, pf)
	}
	return out
}

func queryrunnerPartitionCount(m queryrunner.PlanMetrics) float64 {
	if m.HasPartitionAppend {
		return m.PartitionsScanned
	}
	if m.PartitionsScanned > 0 {
		return m.PartitionsScanned
	}
	return 1
}

// AddCandidate adds a candidate rewrite and compares plans.
func (s *InvestigationsService) AddCandidate(ctx context.Context, payload *investigations.AddCandidatePayload) (*investigations.Investigation, error) {
	inv, err := s.Get(ctx, &investigations.GetPayload{ID: payload.ID})
	if err != nil {
		return nil, err
	}

	// Prefer EXPLAIN ANALYZE for credible before/after timings; fall back if disabled.
	cmp, err := s.queriesSvc.ComparePlans(ctx, &queries.ComparePlansPayload{
		BeforeSQL:    inv.SQL,
		AfterSQL:     payload.CandidateSQL,
		Analyze:      true,
		ConnectionID: &inv.ConnectionID,
		Binds:        payload.Binds,
	})
	if err != nil {
		cmp, err = s.queriesSvc.ComparePlans(ctx, &queries.ComparePlansPayload{
			BeforeSQL:    inv.SQL,
			AfterSQL:     payload.CandidateSQL,
			Analyze:      false,
			ConnectionID: &inv.ConnectionID,
			Binds:        payload.Binds,
		})
	}
	if err != nil {
		return nil, normalizeInvestigationError(err)
	}

	cmpJSON, _ := json.Marshal(cmp)
	candidateExplainJSON, _ := json.Marshal(cmp.After)

	// Cost delta and equivalence, denormalized for listing the candidate history.
	var costDelta *float64
	if cmp.Before != nil && cmp.After != nil {
		d := cmp.After.TotalCost - cmp.Before.TotalCost
		costDelta = &d
	}
	var equivStatus *string
	if cmp.ResultEquivalenceStatus != nil && *cmp.ResultEquivalenceStatus != "" {
		equivStatus = cmp.ResultEquivalenceStatus
	}
	var bindsJSON []byte
	if len(payload.Binds) > 0 {
		bindsJSON, _ = json.Marshal(payload.Binds)
	}

	// Record this candidate in the history (one row per distinct SQL; a re-test
	// refreshes it) and point the investigation at it.
	if _, err := s.appPool.Exec(ctx, `
		INSERT INTO app.investigation_candidates (
			organization_id, investigation_id, candidate_sql, binds,
			candidate_explain, comparison, equivalence_status, cost_delta, source
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'manual')
		ON CONFLICT (investigation_id, md5(candidate_sql)) DO UPDATE SET
			binds = EXCLUDED.binds,
			candidate_explain = EXCLUDED.candidate_explain,
			comparison = EXCLUDED.comparison,
			equivalence_status = EXCLUDED.equivalence_status,
			cost_delta = EXCLUDED.cost_delta,
			updated_at = now()
	`, orgID(ctx), payload.ID, payload.CandidateSQL, bindsJSON,
		candidateExplainJSON, cmpJSON, equivStatus, costDelta); err != nil {
		return nil, err
	}

	_, err = s.appPool.Exec(ctx, `
		UPDATE app.investigations
		SET candidate_sql = $1, candidate_explain = $2, comparison = $3,
		    status = 'comparing', updated_at = now()
		WHERE id = $4 AND organization_id = $5
	`, payload.CandidateSQL, candidateExplainJSON, cmpJSON, payload.ID, orgID(ctx))
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, &investigations.GetPayload{ID: payload.ID})
}

// loadInvestigationCandidates returns the tested-candidate history, newest first,
// marking the one currently attached to the investigation.
func (s *InvestigationsService) loadInvestigationCandidates(ctx context.Context, invID string, currentSQL *string) []*investigations.InvestigationCandidate {
	rows, err := s.appPool.Query(ctx, `
		SELECT id::text, candidate_sql, binds, candidate_explain, comparison,
		       equivalence_status, cost_delta, source, created_at
		FROM app.investigation_candidates
		WHERE investigation_id = $1 AND organization_id = $2
		ORDER BY updated_at DESC
	`, invID, orgID(ctx))
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []*investigations.InvestigationCandidate
	for rows.Next() {
		var c investigations.InvestigationCandidate
		var bindsJSON, explainJSON, cmpJSON []byte
		var equiv, source *string
		var costDelta *float64
		var createdAt time.Time
		if err := rows.Scan(&c.ID, &c.CandidateSQL, &bindsJSON, &explainJSON, &cmpJSON,
			&equiv, &costDelta, &source, &createdAt); err != nil {
			return out
		}
		c.CreatedAt = createdAt.Format(time.RFC3339)
		c.EquivalenceStatus = equiv
		c.CostDelta = costDelta
		if source != nil {
			c.Source = source
		}
		if len(bindsJSON) > 0 && string(bindsJSON) != "null" {
			_ = json.Unmarshal(bindsJSON, &c.Binds)
		}
		if len(explainJSON) > 0 {
			var exp investigations.ExplainQueryResult
			if json.Unmarshal(explainJSON, &exp) == nil {
				c.CandidateExplain = &exp
			}
		}
		if len(cmpJSON) > 0 {
			var cmp investigations.ComparePlansResult
			if json.Unmarshal(cmpJSON, &cmp) == nil {
				c.Comparison = &cmp
			}
		}
		if currentSQL != nil && c.CandidateSQL == *currentSQL {
			cur := true
			c.IsCurrent = &cur
		}
		out = append(out, &c)
	}
	return out
}

// GenerateReport creates an engineering investigation report from collected evidence.
func (s *InvestigationsService) GenerateReport(ctx context.Context, payload *investigations.GenerateReportPayload) (*investigations.Investigation, error) {
	inv, err := s.Get(ctx, &investigations.GetPayload{ID: payload.ID})
	if err != nil {
		return nil, err
	}

	if inv.Comparison != nil {
		status := equivalenceStatusFromComparison(inv.Comparison)
		if status != "Equal" {
			msg := "result equivalence is Unverified — re-run Compare plans until status is Equal before generating a shippable report"
			if status == "Different" {
				msg = "result equivalence is Different — reconcile the candidate rewrite before generating a shippable report"
			}
			return nil, &investigations.ValidationError{
				Name:    "validation_error",
				Message: msg,
				Code:    strPtr("EQUIVALENCE_NOT_EQUAL"),
			}
		}
	}

	var stat story.StatInput
	if inv.StatSnapshot != nil {
		stat = story.StatInput{
			MeanTimeMs:  inv.StatSnapshot.MeanTimeMs,
			TotalTimeMs: inv.StatSnapshot.TotalTimeMs,
			Calls:       inv.StatSnapshot.Calls,
		}
	}

	findings := planFindingsFromInvestigation(inv.Explain)
	var comparison *story.ComparisonInput
	if inv.Comparison != nil {
		metrics := make([]story.ComparisonMetricRow, 0, len(inv.Comparison.Metrics))
		for _, m := range inv.Comparison.Metrics {
			if m == nil {
				continue
			}
			metrics = append(metrics, story.ComparisonMetricRow{
				Evidence: m.Evidence,
				Before:   m.Before,
				After:    m.After,
				Change:   m.Change,
			})
		}
		improved := []string{}
		if inv.Comparison.Diff != nil {
			improved = inv.Comparison.Diff.Improved
		}
		comparison = &story.ComparisonInput{
			Metrics:                 metrics,
			Improved:                improved,
			ResultChecksumEqual:     inv.Comparison.ResultChecksumEqual,
			ResultEquivalenceStatus: ptrString(inv.Comparison.ResultEquivalenceStatus),
			ResultEquivalenceNotes:  ptrString(inv.Comparison.ResultEquivalenceNotes),
			ResultBeforeRowCount:    inv.Comparison.ResultBeforeRowCount,
			ResultAfterRowCount:     inv.Comparison.ResultAfterRowCount,
			ResultSampleRows:        inv.Comparison.ResultSampleRows,
		}
	}

	candidateSQL := ""
	if inv.CandidateSQL != nil {
		candidateSQL = *inv.CandidateSQL
	}
	fingerprint := ""
	if inv.QueryFingerprint != nil {
		fingerprint = *inv.QueryFingerprint
	}

	invReport, narrative := story.BuildInvestigationReport(
		inv.Title, inv.SQL, candidateSQL, fingerprint, inv.ConnectionID,
		stat, findings, comparison,
		story.InvestigationProvenance{
			QueryFingerprint: fingerprint,
			GeneratedBy:      auth.PrincipalFromContext(ctx).UserID,
		},
	)

	report, err := s.reportsSvc.StoreInvestigationReport(ctx, inv, invReport, narrative)
	if err != nil {
		return nil, normalizeInvestigationError(err)
	}

	_, err = s.appPool.Exec(ctx, `
		UPDATE app.investigations
		SET report_id = $1, status = 'complete', updated_at = now()
		WHERE id = $2 AND organization_id = $3
	`, report.ID, payload.ID, orgID(ctx))
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, &investigations.GetPayload{ID: payload.ID})
}

func planFindingsFromInvestigation(exp *investigations.ExplainQueryResult) []story.PlanFindingInput {
	if exp == nil {
		return nil
	}
	out := make([]story.PlanFindingInput, 0, len(exp.Findings))
	for _, f := range exp.Findings {
		if f == nil {
			continue
		}
		conf := ""
		if f.Confidence != nil {
			conf = *f.Confidence
		}
		cat := ""
		if f.Category != nil {
			cat = *f.Category
		}
		out = append(out, story.PlanFindingInput{
			NodeType:   f.NodeType,
			Category:   cat,
			Confidence: conf,
			Message:    f.Message,
			Evidence:   f.Evidence,
		})
	}
	return out
}

func sqlFingerprint(sql string) string {
	normalized := strings.Join(strings.Fields(strings.TrimSpace(strings.ToLower(sql))), " ")
	h := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(h[:8])
}

func equivalenceStatusFromComparison(cmp *investigations.ComparePlansResult) string {
	if cmp == nil {
		return "Unverified"
	}
	if cmp.ResultEquivalenceStatus != nil && *cmp.ResultEquivalenceStatus != "" {
		return *cmp.ResultEquivalenceStatus
	}
	if cmp.ResultChecksumEqual == nil {
		return "Unverified"
	}
	if *cmp.ResultChecksumEqual {
		return "Equal"
	}
	return "Different"
}

func normalizeInvestigationError(err error) error {
	if err == nil {
		return nil
	}
	var qv *queries.ValidationError
	if errors.As(err, &qv) {
		return &investigations.ValidationError{Name: qv.Name, Message: qv.Message, Code: qv.Code}
	}
	var rv *reports.ValidationError
	if errors.As(err, &rv) {
		return &investigations.ValidationError{Name: rv.Name, Message: rv.Message, Code: rv.Code}
	}
	return err
}

// Ensure InvestigationsService implements investigations.Service at compile time.
var _ investigations.Service = (*InvestigationsService)(nil)
