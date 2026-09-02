package service

import (
	"context"
	"encoding/json"

	"github.com/pgquerynarrative/pgquerynarrative/api/gen/investigations"
	"github.com/pgquerynarrative/pgquerynarrative/api/gen/reports"
	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/app/story"
)

// StoreInvestigationReport persists an evidence-backed Query Investigation report.
func (s *ReportsService) StoreInvestigationReport(
	ctx context.Context,
	inv *investigations.Investigation,
	invReport *story.InvestigationReport,
	narrative *story.NarrativeContent,
) (*reports.Report, error) {
	if inv == nil || invReport == nil || narrative == nil {
		return nil, &reports.ValidationError{Name: "validation_error", Message: "investigation report payload required", Code: strPtr("VALIDATION_ERROR")}
	}

	narrativeJSON, _ := json.Marshal(narrative)
	metricsPayload := map[string]any{
		"investigation": invReport,
	}
	metricsJSON, _ := json.Marshal(metricsPayload)
	statsJSON, _ := json.Marshal(map[string]any{
		"report_type":       "query_investigation",
		"investigation_id":  inv.ID,
		"query_fingerprint": inv.QueryFingerprint,
	})

	var reportID string
	p := auth.PrincipalFromContext(ctx)
	sqlAtRest, sealErr := sealProductSQL(s.dataEncKey, inv.SQL)
	if sealErr != nil {
		return nil, sealErr
	}

	err := s.appPool.QueryRow(ctx, `
		INSERT INTO app.reports (
			sql, narrative_md, narrative_json, metrics, stats,
			llm_model, llm_provider, success, connection_id, organization_id, created_by
		) VALUES ($1, $2, $3, $4, $5, $6, $7, true, $8, $9, $10)
		RETURNING id
	`, sqlAtRest, narrative.Headline, narrativeJSON, metricsJSON, statsJSON,
		"evidence-template", "pgquerynarrative", inv.ConnectionID, p.OrgID, p.UserID).Scan(&reportID)
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, &reports.GetPayload{ID: reportID})
}
