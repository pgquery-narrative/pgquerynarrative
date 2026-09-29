package auth

import (
	"net/http"
	"strings"
)

// RequireAdmin allows platform or tenant admins and rejects other roles.
func RequireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if !IsAdminRole(RoleFromContext(r.Context())) {
		WriteForbidden(w)
		return false
	}
	return true
}

// RequirePlatformAdmin allows only platform-wide administrators.
func RequirePlatformAdmin(w http.ResponseWriter, r *http.Request) bool {
	if !IsPlatformAdminRole(RoleFromContext(r.Context())) {
		WriteForbidden(w)
		return false
	}
	return true
}

// ResolveAdminOrgScope returns the organisation ID an admin request may act on.
// Tenant administrators always operate on Principal.OrgID; caller-supplied
// organisation IDs are ignored so they cannot target another organisation.
// Platform administrators may target any organisation_id.
func ResolveAdminOrgScope(w http.ResponseWriter, r *http.Request, requestedOrgID string) (string, bool) {
	p := PrincipalFromContext(r.Context())
	if IsTenantAdminRole(p.Role) {
		orgID := strings.TrimSpace(p.OrgID)
		if orgID == "" {
			WriteForbidden(w)
			return "", false
		}
		return orgID, true
	}
	if !IsPlatformAdminRole(p.Role) {
		WriteForbidden(w)
		return "", false
	}
	orgID := strings.TrimSpace(requestedOrgID)
	if orgID == "" {
		orgID = strings.TrimSpace(p.OrgID)
	}
	if orgID == "" {
		WriteForbidden(w)
		return "", false
	}
	return orgID, true
}
