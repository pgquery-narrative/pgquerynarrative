package auth

import (
	"net/http"
	"strings"
)

// ValidateRequest is a legacy helper for single API key auth. Prefer Authenticator.
func ValidateRequest(r *http.Request, expectedAPIKey string) (identity string, ok bool) {
	enabled := strings.TrimSpace(expectedAPIKey) != ""
	a := NewAuthenticator(enabled, expectedAPIKey, "", nil)
	id, _, ok := a.ValidateRequest(r)
	return id, ok
}

// ForbiddenJSON is the standard RBAC denial response body.
const ForbiddenJSON = `{"name":"forbidden","message":"insufficient role for this operation","code":"FORBIDDEN"}`

// WriteForbidden writes a 403 JSON response.
func WriteForbidden(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(ForbiddenJSON))
}
