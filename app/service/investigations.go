package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pgquerynarrative/pgquerynarrative/api/gen/queries"
	"github.com/pgquerynarrative/pgquerynarrative/api/gen/reports"
	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/app/db"
	"github.com/pgquerynarrative/pgquerynarrative/app/queryrunner"
	"github.com/pgquerynarrative/pgquerynarrative/app/story"
	"github.com/pgquerynarrative/pgquerynarrative/gen/investigations"
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
		       comparison, report_id, created_at, updated_at
		FROM app.investigations
		WHERE id = $1 AND organization_id = $2
	`, payload.ID, orgID(ctx))

	var inv investigations.Investigation
	var statJSON, explainJSON, candidateExplainJSON, comparisonJSON []byte
	var candidateSQL, reportID, fingerprint *string
	var createdAt, updatedAt time.Time

	err := row.Scan(
		&inv.ID, &inv.Title, &inv.Status, &inv.SQL, &inv.ConnectionID, &fingerprint,
		&statJSON, &explainJSON, &candidateSQL, &candidateExplainJSON,
		&comparisonJSON, &reportID, &createdAt, &updatedAt,
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
	return &inv, nil
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

	findings := queryrunnerFindingsFromInvestigation(inv.Explain)
	if len(findings) == 0 {
		// Fall back to freshly explained findings (includes IndexAdvice).
		findings = queryrunnerFindingsFromQueriesAPI(baselineAPI)
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
	scored = append(scored, queryrunner.CollectIndexDDLCandidates(findings)...)
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
	if exp.ExecutionTimeMs > 0 && !m.HasActualTiming {
		m.ExecutionTimeMs = float64(exp.ExecutionTimeMs)
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
	})
	if err != nil {
		cmp, err = s.queriesSvc.ComparePlans(ctx, &queries.ComparePlansPayload{
			BeforeSQL:    inv.SQL,
			AfterSQL:     payload.CandidateSQL,
			Analyze:      false,
			ConnectionID: &inv.ConnectionID,
		})
	}
	if err != nil {
		return nil, normalizeInvestigationError(err)
	}

	cmpJSON, _ := json.Marshal(cmp)
	candidateExplainJSON, _ := json.Marshal(cmp.After)

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

// GenerateReport creates an engineering investigation report from collected evidence.
func (s *InvestigationsService) GenerateReport(ctx context.Context, payload *investigations.GenerateReportPayload) (*investigations.Investigation, error) {
	inv, err := s.Get(ctx, &investigations.GetPayload{ID: payload.ID})
	if err != nil {
		return nil, err
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
			Metrics:             metrics,
			Improved:            improved,
			ResultChecksumEqual: inv.Comparison.ResultChecksumEqual,
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
