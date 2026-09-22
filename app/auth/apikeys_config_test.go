package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

const validHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestParseAPIKeysJSONRejectsEveryMistakeThatWouldWeakenAKey(t *testing.T) {
	cases := map[string]string{
		"invalid JSON":            `not json`,
		"misspelled field":        `[{"keyhash":"` + validHash + `","role":"viewer"}]`,
		"no key and no hash":      `[{"id":"a","role":"viewer"}]`,
		"short hash":              `[{"key_hash":"abc","role":"viewer"}]`,
		"missing role":            `[{"key_hash":"` + validHash + `"}]`,
		"unknown role read-only":  `[{"key_hash":"` + validHash + `","role":"read-only"}]`,
		"unknown role guest":      `[{"key_hash":"` + validHash + `","role":"guest"}]`,
		"date-only expires_at":    `[{"key_hash":"` + validHash + `","role":"viewer","expires_at":"2020-01-01"}]`,
		"unknown scope":           `[{"key_hash":"` + validHash + `","role":"viewer","scopes":["bogus"]}]`,
		"revoked as a string":     `[{"key_hash":"` + validHash + `","role":"viewer","revoked":"false"}]`,
		"trailing data":           `[{"key_hash":"` + validHash + `","role":"viewer"}] []`,
		"an object, not an array": `{"key_hash":"` + validHash + `","role":"viewer"}`,
	}
	for name, raw := range cases {
		if _, err := ParseAPIKeysJSON(raw); err == nil {
			t.Errorf("%s: accepted %s", name, raw)
		}
	}
}

func TestParseAPIKeysJSONAcceptsAGoodKey(t *testing.T) {
	keys, err := ParseAPIKeysJSON(`[{"key_hash":"` + validHash + `","id":"ci","role":"read","scopes":["read"],"expires_at":"2030-01-01T00:00:00Z","org_id":"org-1"}]`)
	if err != nil || len(keys) != 1 {
		t.Fatalf("keys=%v err=%v", keys, err)
	}
	if keys[0].ExpiresAt.IsZero() || keys[0].ID != "ci" {
		t.Errorf("entry = %+v", keys[0])
	}
	if got, err := ParseAPIKeysJSON("  "); got != nil || err != nil {
		t.Errorf("empty input = %v, %v", got, err)
	}
}

func TestValidateCredentialSources(t *testing.T) {
	ok := `[{"key_hash":"` + validHash + `","role":"viewer"}]`
	for name, c := range map[string]struct {
		enabled bool
		apiKey  string
		hash    string
		json    string
		oidc    bool
		wantErr bool
	}{
		"empty array is not a credential":       {true, "", "", `[]`, false, true},
		"garbage JSON":                          {true, "", "", `oops`, false, true},
		"one good key":                          {true, "", "", ok, false, false},
		"empty array plus OIDC":                 {true, "", "", `[]`, true, false},
		"empty array plus a plain key":          {true, "a-long-enough-key-1", "", `[]`, false, false},
		"bad primary hash":                      {true, "", "zz", "", false, true},
		"disabled needs nothing":                {false, "", "", "", false, false},
		"disabled still refuses malformed JSON": {false, "", "", `oops`, false, true},
	} {
		err := ValidateCredentialSources(c.enabled, c.apiKey, c.hash, c.json, c.oidc)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err=%v wantErr=%v", name, err, c.wantErr)
		}
	}
}

// An enabled server whose keys did not load must answer 401, not run as an open admin.
func TestEnabledWithUnusableKeysFailsClosed(t *testing.T) {
	for _, raw := range []string{`oops`, `[]`, `[{"keyhash":"` + validHash + `","role":"viewer"}]`} {
		a := NewAuthenticator(true, "", "", raw, nil)
		if !a.AuthRequired() {
			t.Errorf("%s: AuthRequired() = false, the middleware would serve the default admin", raw)
		}
		r := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
		r.Header.Set("Authorization", "Bearer anything")
		if _, ok := a.ValidatePrincipal(r); ok {
			t.Errorf("%s: a bearer token was accepted", raw)
		}
	}
}

func TestUnknownRolesAreNeverWriters(t *testing.T) {
	for _, role := range []string{"read-only", "read_only", "guest", "", "Owner", "viewer "} {
		got := normalizeRole(role)
		if got == RoleAnalyst || IsAdminRole(got) {
			t.Errorf("normalizeRole(%q) = %q, must not gain write access", role, got)
		}
	}
	if normalizeRole("Analyst ") != RoleAnalyst || normalizeRole("READER") != RoleViewer {
		t.Error("known spellings must still normalize")
	}
	if IsKnownRole("read-only") || IsKnownRole("") || !IsKnownRole("readonly") {
		t.Error("IsKnownRole disagrees with the alias table")
	}
}
