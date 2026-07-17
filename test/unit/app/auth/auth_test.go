package auth_test

import (
	"net/http"
	"testing"

	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
)

func TestValidateRequest(t *testing.T) {
	tests := []struct {
		name           string
		expectedAPIKey string
		authHeader     string
		wantOK         bool
		wantIdentity   string
	}{
		{"empty key allows all", "", "", true, "system"},
		{"empty key allows with header", "", "Bearer anything", true, "system"},
		{"no header when key set", "secret", "", false, ""},
		{"wrong prefix", "secret", "Basic dXNlcjpwYXNz", false, ""},
		{"Bearer but empty token", "secret", "Bearer ", false, ""},
		{"Bearer with wrong token", "secret", "Bearer wrong", false, ""},
		{"Bearer with correct token", "secret", "Bearer secret", true, "api-key"},
		{"Bearer token with extra spaces", "secret", "  Bearer   secret  ", true, "api-key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, _ := http.NewRequest(http.MethodGet, "/", nil)
			if tt.authHeader != "" {
				r.Header.Set("Authorization", tt.authHeader)
			}
			identity, ok := auth.ValidateRequest(r, tt.expectedAPIKey)
			if ok != tt.wantOK {
				t.Errorf("ValidateRequest() ok = %v, want %v", ok, tt.wantOK)
			}
			if identity != tt.wantIdentity {
				t.Errorf("ValidateRequest() identity = %q, want %q", identity, tt.wantIdentity)
			}
		})
	}
}
