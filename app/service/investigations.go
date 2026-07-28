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
		Analyze:      false,
		ConnectionID: &connID,
	})
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

// AddCandidate adds a candidate rewrite and compares plans.
func (s *InvestigationsService) AddCandidate(ctx context.Context, payload *investigations.AddCandidatePayload) (*investigations.Investigation, error) {
	inv, err := s.Get(ctx, &investigations.GetPayload{ID: payload.ID})
	if err != nil {
		return nil, err
	}

	cmp, err := s.queriesSvc.ComparePlans(ctx, &queries.ComparePlansPayload{
		BeforeSQL:    inv.SQL,
		AfterSQL:     payload.CandidateSQL,
		Analyze:      payload.Analyze,
		ConnectionID: &inv.ConnectionID,
	})
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
