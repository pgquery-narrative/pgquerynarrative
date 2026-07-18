package auth_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
)

func TestHashAPIKeyMatches(t *testing.T) {
	secret := "super-secret-key"
	hash := auth.HashAPIKey(secret)
	a := auth.NewAuthenticator(true, "", hash, "", nil)
	r, _ := http.NewRequest(http.MethodGet, "/api/v1/queries/history", nil)
	r.Header.Set("Authorization", "Bearer "+secret)
	_, ok := a.ValidatePrincipal(r)
	if !ok {
		t.Fatal("expected hashed API key to authenticate")
	}
}

func TestAPIKeyExpiry(t *testing.T) {
	secret := "expiring-key"
	hash := auth.HashAPIKey(secret)
	expired := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	keysJSON := `[{"key_hash":"` + hash + `","id":"expired","role":"admin","expires_at":"` + expired + `"}]`
	a := auth.NewAuthenticator(true, "", "", keysJSON, nil)
	r, _ := http.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+secret)
	if _, ok := a.ValidatePrincipal(r); ok {
		t.Fatal("expected expired API key to be rejected")
	}
}

func TestAPIKeyRevoked(t *testing.T) {
	secret := "revoked-key"
	a := auth.NewAuthenticator(true, secret, "", `[{"key":"`+secret+`","id":"revoked","role":"admin","revoked":true}]`, nil)
	r, _ := http.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+secret)
	if _, ok := a.ValidatePrincipal(r); ok {
		t.Fatal("revoked API key should be rejected")
	}
}

func TestAPIKeyMetadataTracksLastUsedAndPrefix(t *testing.T) {
	secret := "abcdefgh12345678"
	a := auth.NewAuthenticator(true, "", "", `[{"key_hash":"`+auth.HashAPIKey(secret)+`","id":"tracked","role":"admin","prefix":"abcdefgh"}]`, nil)

	before := a.KeyMetadataList()
	if len(before) != 1 {
		t.Fatalf("expected 1 key, got %d", len(before))
	}
	if before[0].Prefix != "abcdefgh" {
		t.Fatalf("expected configured prefix to be preserved, got %q", before[0].Prefix)
	}
	if !before[0].LastUsedAt.IsZero() {
		t.Fatal("expected no last-used timestamp before first use")
	}

	r, _ := http.NewRequest(http.MethodGet, "/api/v1/queries/history", nil)
	r.Header.Set("Authorization", "Bearer "+secret)
	if _, ok := a.ValidatePrincipal(r); !ok {
		t.Fatal("expected key to authenticate")
	}

	after := a.KeyMetadataList()
	if after[0].LastUsedAt.IsZero() {
		t.Fatal("expected last-used timestamp to be set after successful auth")
	}
	if _, ok := a.LastUsedAt("tracked"); !ok {
		t.Fatal("expected LastUsedAt to report usage for tracked key")
	}
}

func TestAPIKeyMetadataDerivesPrefixFromPlainKey(t *testing.T) {
	secret := "plain-secret-key-value"
	a := auth.NewAuthenticator(true, "", "", `[{"key":"`+secret+`","id":"plain","role":"analyst"}]`, nil)
	meta := a.KeyMetadataList()
	if len(meta) != 1 || meta[0].Prefix == "" {
		t.Fatalf("expected derived prefix for plain-key entry, got %+v", meta)
	}
	if meta[0].Prefix != secret[:8] {
		t.Fatalf("expected prefix %q, got %q", secret[:8], meta[0].Prefix)
	}
}

func TestAPIKeyScopesReadOnly(t *testing.T) {
	secret := "scoped-read"
	a := auth.NewAuthenticator(true, secret, "", `[{"key":"`+secret+`","id":"reader","role":"viewer","scopes":["read"]}]`, nil)
	post, _ := http.NewRequest(http.MethodPost, "/api/v1/queries/run", nil)
	post.Header.Set("Authorization", "Bearer "+secret)
	if _, ok := a.ValidatePrincipal(post); ok {
		t.Fatal("read scope should not allow POST run")
	}
	get, _ := http.NewRequest(http.MethodGet, "/api/v1/queries/history", nil)
	get.Header.Set("Authorization", "Bearer "+secret)
	if _, ok := a.ValidatePrincipal(get); !ok {
		t.Fatal("read scope should allow GET")
	}
}
