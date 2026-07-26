package security_test

import (
	"testing"

	"github.com/pgquerynarrative/pgquerynarrative/app/security"
)

func TestValidateWebhookHostAllowlist_AllowsSuffix(t *testing.T) {
	if err := security.ValidateWebhookHostAllowlist("https://hooks.example.com/path", []string{"example.com"}); err != nil {
		t.Fatalf("expected allow: %v", err)
	}
}

func TestValidateWebhookHostAllowlist_BlocksUnknownHost(t *testing.T) {
	if err := security.ValidateWebhookHostAllowlist("https://evil.example.net/path", []string{"example.com"}); err == nil {
		t.Fatal("expected block for unknown host")
	}
}

func TestValidateWebhookHostAllowlist_EmptyAllowlistFailsClosed(t *testing.T) {
	if err := security.ValidateWebhookHostAllowlist("https://hooks.example.com/path", nil); err == nil {
		t.Fatal("empty allowlist must fail closed")
	}
	if err := security.ValidateWebhookHostAllowlist("https://hooks.example.com/path", []string{}); err == nil {
		t.Fatal("empty allowlist must fail closed")
	}
}
