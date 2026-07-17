package auth

import (
	"context"
	"os"
	"strings"
)

// DefaultOrganizationID is the bootstrap org used for single-tenant and dev deployments.
const DefaultOrganizationID = "00000000-0000-0000-0000-000000000001"

// OrgContextKey stores the active organization ID in request context.
const OrgContextKey ContextKey = "auth_org_id"

// Principal is the authenticated actor for a request.
type Principal struct {
	UserID string
	OrgID  string
	Role   string
}

// DefaultOrgID returns DEFAULT_ORGANIZATION_ID or the bootstrap org constant.
func DefaultOrgID() string {
	if v := strings.TrimSpace(os.Getenv("DEFAULT_ORGANIZATION_ID")); v != "" {
		return v
	}
	return DefaultOrganizationID
}

// PrincipalFromContext returns the request principal, falling back to the default org and admin role.
func PrincipalFromContext(ctx context.Context) Principal {
	p := Principal{
		UserID: "system",
		OrgID:  DefaultOrgID(),
		Role:   RoleAdmin,
	}
	if v, ok := ctx.Value(IdentityContextKey).(string); ok && v != "" {
		p.UserID = v
	}
	if v, ok := ctx.Value(OrgContextKey).(string); ok && v != "" {
		p.OrgID = v
	}
	if v, ok := ctx.Value(RoleContextKey).(string); ok && v != "" {
		p.Role = v
	}
	return p
}

// OrgIDFromContext returns the organization ID for the current request.
// Missing org context fails closed (empty string) so RLS policies expose no rows.
func OrgIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(OrgContextKey).(string); ok {
		if id := strings.TrimSpace(v); id != "" {
			return id
		}
	}
	return ""
}

// WithPrincipal stores principal fields on the context.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	ctx = context.WithValue(ctx, IdentityContextKey, p.UserID)
	ctx = context.WithValue(ctx, OrgContextKey, p.OrgID)
	ctx = context.WithValue(ctx, RoleContextKey, p.Role)
	return ctx
}
