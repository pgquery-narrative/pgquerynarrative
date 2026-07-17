package service

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pgquerynarrative/pgquerynarrative/api/gen/reports"
	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
)

// CreateShare creates or refreshes a shareable read-only token for a report.
func (s *ReportsService) CreateShare(ctx context.Context, payload *reports.CreateSharePayload) (*reports.ReportShareLink, error) {
	if !s.shareLinksEnabled {
		return nil, &reports.ValidationError{Name: "share_links_disabled", Message: "shared report links are disabled", Code: strPtr("SHARE_LINKS_DISABLED")}
	}
	var exists bool
	if err := s.appPool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM app.reports WHERE id = $1 AND organization_id = $2)`, payload.ReportID, orgID(ctx)).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, &reports.NotFoundError{Name: "not_found", Message: "report not found", Code: strPtr("NOT_FOUND")}
	}
	token, err := newShareToken()
	if err != nil {
		return nil, err
	}
	expiresHours := 168
	if s.shareLinkDefaultHours > 0 {
		expiresHours = s.shareLinkDefaultHours
	}
	if payload.ExpiresInHours != nil && *payload.ExpiresInHours > 0 {
		expiresHours = int(*payload.ExpiresInHours)
	}
	if expiresHours > 8760 {
		return nil, &reports.ValidationError{Name: "validation_error", Message: "expires_in_hours must be at most 8760 (1 year)", Code: strPtr("VALIDATION_ERROR")}
	}
	var expiresAt sql.NullTime
	err = s.appPool.QueryRow(ctx, `
		INSERT INTO app.report_share_tokens (report_id, organization_id, token, expires_at)
		VALUES ($1, $2, $3, NOW() + ($4::text || ' hours')::interval)
		ON CONFLICT (report_id) DO UPDATE SET
			token = EXCLUDED.token,
			expires_at = EXCLUDED.expires_at,
			organization_id = EXCLUDED.organization_id,
			created_at = NOW()
		RETURNING expires_at
	`, payload.ReportID, orgID(ctx), token, expiresHours).Scan(&expiresAt)
	if err != nil {
		return nil, err
	}
	link := &reports.ReportShareLink{
		Token: token,
		URL:   "/shared/" + token,
	}
	if expiresAt.Valid {
		t := expiresAt.Time.UTC().Format(time.RFC3339)
		link.ExpiresAt = &t
	}
	return link, nil
}

// GetShared fetches a report through a valid share token.
// Token resolution is org-independent (SECURITY DEFINER); the report is then loaded
// under the owning organization so unauthenticated callers are not tied to default-org.
func (s *ReportsService) GetShared(ctx context.Context, payload *reports.GetSharedPayload) (*reports.Report, error) {
	var reportID, tokenOrg string
	err := s.appPool.QueryRow(ctx, `
		SELECT report_id::text, organization_id::text
		FROM app.resolve_report_share_token($1)
	`, payload.Token).Scan(&reportID, &tokenOrg)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, &reports.NotFoundError{Name: "not_found", Message: "shared report link is invalid or expired", Code: strPtr("NOT_FOUND")}
		}
		return nil, err
	}
	shareCtx := auth.WithPrincipal(ctx, auth.Principal{
		UserID: "share-token",
		OrgID:  tokenOrg,
		Role:   auth.RoleViewer,
	})
	return s.Get(shareCtx, &reports.GetPayload{ID: reportID})
}

func newShareToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
