package security_test

import (
	"testing"

	"github.com/pgquerynarrative/pgquerynarrative/app/security"
)

func TestValidateWebhookHostAllowlist_AllowsSuffix(t *testing.T) {
	if err := security.ValidateWebhookHostAllowlist("https://hooks.example.com/path", []string{"example.com"}); err != nil {
		t.Fatalf("expected allowlist match: %v", err)
	}
}

func TestValidateWebhookHostAllowlist_BlocksUnknownHost(t *testing.T) {
	if err := security.ValidateWebhookHostAllowlist("https://evil.example.net/path", []string{"example.com"}); err == nil {
		t.Fatal("expected allowlist rejection")
	}
}

func TestValidateWebhookHostAllowlist_EmptyAllowlistAllowsAll(t *testing.T) {
	if err := security.ValidateWebhookHostAllowlist("https://hooks.example.com/path", nil); err != nil {
		t.Fatalf("empty allowlist should allow: %v", err)
	}
}
