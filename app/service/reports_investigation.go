package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/pgquerynarrative/pgquerynarrative/api/gen/investigations"
	"github.com/pgquerynarrative/pgquerynarrative/api/gen/reports"
	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/app/db"
	"github.com/pgquerynarrative/pgquerynarrative/app/story"
)

// InsertInvestigationReport writes the report row using the supplied executor
// and returns its id. Taking an executor lets the caller commit the report and
// the investigation's completion in one transaction: persisting the report and
// then failing to mark the investigation complete would leave a stored report
// attached to an investigation that still looks unfinished.
//
// Report evidence is the product. Marshal errors here are therefore returned,
// never dropped — a report that silently persisted "null" narrative or metrics
// would be worse than no report at all.
func (s *ReportsService) InsertInvestigationReport(
	ctx context.Context,
	exec db.DB,
	inv *investigations.Investigation,
	invReport *story.InvestigationReport,
	narrative *story.NarrativeContent,
) (string, error) {
	if inv == nil || invReport == nil || narrative == nil {
		return "", &reports.ValidationError{Name: "validation_error", Message: "investigation report payload required", Code: strPtr("VALIDATION_ERROR")}
	}

	narrativeJSON, err := json.Marshal(narrative)
	if err != nil {
		return "", fmt.Errorf("marshal report narrative: %w", err)
	}
	metricsPayload := map[string]any{
		"investigation": invReport,
	}
	metricsJSON, err := json.Marshal(metricsPayload)
	if err != nil {
		return "", fmt.Errorf("marshal report metrics: %w", err)
	}
	statsJSON, err := json.Marshal(map[string]any{
		"report_type":       "query_investigation",
		"investigation_id":  inv.ID,
		"query_fingerprint": inv.QueryFingerprint,
	})
	if err != nil {
		return "", fmt.Errorf("marshal report stats: %w", err)
	}

	var reportID string
	p := auth.PrincipalFromContext(ctx)
	sqlAtRest, sealErr := sealProductSQL(s.dataEncKey, inv.SQL)
	if sealErr != nil {
		return "", sealErr
	}

	err = exec.QueryRow(ctx, `
		INSERT INTO app.reports (
			sql, narrative_md, narrative_json, metrics, stats,
			llm_model, llm_provider, success, connection_id, organization_id, created_by
		) VALUES ($1, $2, $3, $4, $5, $6, $7, true, $8, $9, $10)
		RETURNING id
	`, sqlAtRest, narrative.Headline, narrativeJSON, metricsJSON, statsJSON,
		"evidence-template", "pgquerynarrative", inv.ConnectionID, p.OrgID, p.UserID).Scan(&reportID)
	if err != nil {
		return "", err
	}
	return reportID, nil
}
