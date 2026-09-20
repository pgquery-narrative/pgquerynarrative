package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pgquerynarrative/pgquerynarrative/api/gen/reports"
	"github.com/pgquerynarrative/pgquerynarrative/app/audit"
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
	tokenHash := hashShareToken(token)
	var expiresAt sql.NullTime
	var shareID string
	// Multiple active links per report are intentional (e.g. rotated or
	// separately revocable links); the plaintext token is never persisted,
	// only its hash, so it cannot be recovered from the database.
	err = s.appPool.QueryRow(ctx, `
		INSERT INTO app.report_share_tokens (report_id, organization_id, token_hash, expires_at)
		VALUES ($1, $2, $3, NOW() + make_interval(hours => $4::int))
		RETURNING id::text, expires_at
	`, payload.ReportID, orgID(ctx), tokenHash, expiresHours).Scan(&shareID, &expiresAt)
	if err != nil {
		return nil, err
	}
	if s.auditStore != nil {
		id := shareID
		p := auth.PrincipalFromContext(ctx)
		_ = s.auditStore.Record(ctx, audit.Entry{
			EventType:  audit.EventCreateShare,
			EntityType: "report_share",
			EntityID:   &id,
			UserID:     p.UserID,
			HighRisk:   true,
			Details:    map[string]interface{}{"report_id": payload.ReportID},
		})
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
	`, hashShareToken(payload.Token)).Scan(&reportID, &tokenOrg)
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
	report, err := s.Get(shareCtx, &reports.GetPayload{ID: reportID})
	if err != nil {
		return nil, err
	}
	// Unauthenticated share-link viewers see the narrative/metrics/charts but not the raw
	// SQL by default, since it may reveal schema or business-logic details beyond what the
	// report was meant to share. Operators can opt back in via SetShareLinkExposeSQL.
	if report != nil && !s.shareLinkExposeSQL {
		report.SQL = ""
	}
	return report, nil
}

// ListShares returns non-secret metadata for every share link (active or revoked) created
// for a report the caller's organization owns. The raw token and its hash are never
// returned; only the fields an owner needs to audit and decide whether to revoke a link.
func (s *ReportsService) ListShares(ctx context.Context, payload *reports.ListSharesPayload) (*reports.ReportShareList, error) {
	var exists bool
	if err := s.appPool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM app.reports WHERE id = $1 AND organization_id = $2)`, payload.ReportID, orgID(ctx)).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, &reports.NotFoundError{Name: "not_found", Message: "report not found", Code: strPtr("NOT_FOUND")}
	}
	rows, err := s.appPool.Query(ctx, `
		SELECT id::text, created_at, expires_at, revoked_at, access_count, last_accessed_at
		FROM app.report_share_tokens
		WHERE report_id = $1 AND organization_id = $2
		ORDER BY created_at DESC
	`, payload.ReportID, orgID(ctx))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]*reports.ReportShareInfo, 0)
	for rows.Next() {
		var (
			id                                 string
			createdAt                          time.Time
			expiresAt, revokedAt, lastAccessed sql.NullTime
			accessCount                        int32
		)
		if err := rows.Scan(&id, &createdAt, &expiresAt, &revokedAt, &accessCount, &lastAccessed); err != nil {
			return nil, err
		}
		info := &reports.ReportShareInfo{
			ID:          id,
			CreatedAt:   createdAt.UTC().Format(time.RFC3339),
			AccessCount: accessCount,
		}
		if expiresAt.Valid {
			t := expiresAt.Time.UTC().Format(time.RFC3339)
			info.ExpiresAt = &t
		}
		if revokedAt.Valid {
			t := revokedAt.Time.UTC().Format(time.RFC3339)
			info.RevokedAt = &t
		}
		if lastAccessed.Valid {
			t := lastAccessed.Time.UTC().Format(time.RFC3339)
			info.LastAccessedAt = &t
		}
		items = append(items, info)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &reports.ReportShareList{Items: items}, nil
}

// RevokeShare marks a share link revoked so it can no longer be used to fetch its report,
// without deleting the audit trail (access_count, last_accessed_at) already recorded on it.
func (s *ReportsService) RevokeShare(ctx context.Context, payload *reports.RevokeSharePayload) error {
	tag, err := s.appPool.Exec(ctx, `
		UPDATE app.report_share_tokens
		SET revoked_at = NOW()
		WHERE id = $1 AND organization_id = $2 AND revoked_at IS NULL
	`, payload.ID, orgID(ctx))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return &reports.NotFoundError{Name: "not_found", Message: "share link not found or already revoked", Code: strPtr("NOT_FOUND")}
	}
	if s.auditStore != nil {
		id := payload.ID
		p := auth.PrincipalFromContext(ctx)
		_ = s.auditStore.Record(ctx, audit.Entry{
			EventType:  audit.EventRevokeShare,
			EntityType: "report_share",
			EntityID:   &id,
			UserID:     p.UserID,
			HighRisk:   true,
		})
	}
	return nil
}

func newShareToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// hashShareToken derives the lookup key stored in app.report_share_tokens.token_hash.
// The plaintext token is never persisted; only its SHA-256 hex digest is, so a
// database read (or backup leak) cannot be used to reconstruct valid share links.
func hashShareToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
