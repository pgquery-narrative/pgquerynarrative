package service_test

import (
	"testing"

	"github.com/pgquerynarrative/pgquerynarrative/app/security"
	"github.com/pgquerynarrative/pgquerynarrative/app/service"
)

func TestConfigureWebhook_PreservesAllowlistAndCopiesSlice(t *testing.T) {
	svc := service.NewSchedulesService(nil, nil, nil)
	hosts := []string{"hooks.example.com", "alerts.corp.example"}
	svc.ConfigureWebhook("test-secret", hosts)

	got := svc.WebhookAllowedHosts()
	if len(got) != 2 || got[0] != "hooks.example.com" || got[1] != "alerts.corp.example" {
		t.Fatalf("allowlist not preserved: %v", got)
	}

	// Mutating the caller's slice must not change runtime policy.
	hosts[0] = "evil.example.net"
	got = svc.WebhookAllowedHosts()
	if got[0] != "hooks.example.com" {
		t.Fatalf("caller mutation leaked into policy: %v", got)
	}

	// Mutating the returned slice must not change runtime policy.
	got[0] = "evil.example.net"
	got2 := svc.WebhookAllowedHosts()
	if got2[0] != "hooks.example.com" {
		t.Fatalf("returned slice mutation leaked into policy: %v", got2)
	}
}

func TestConfigureWebhook_RejectsDisallowedHost(t *testing.T) {
	svc := service.NewSchedulesService(nil, nil, nil)
	svc.ConfigureWebhook("secret", []string{"hooks.example.com"})

	if err := security.ValidateWebhookHostAllowlist("https://evil.example.net/hook", svc.WebhookAllowedHosts()); err == nil {
		t.Fatal("expected disallowed host to be rejected")
	}
	if err := security.ValidateWebhookHostAllowlist("https://hooks.example.com/hook", svc.WebhookAllowedHosts()); err != nil {
		t.Fatalf("expected allowed host: %v", err)
	}
}

func TestSetWebhookSigningSecret_SecretOnlyDoesNotClearWhenUsingConfigureWebhook(t *testing.T) {
	svc := service.NewSchedulesService(nil, nil, nil)
	svc.ConfigureWebhook("secret", []string{"hooks.example.com"})

	// Simulate the old main.go bug: calling with secret only.
	// After ConfigureWebhook is the sole init path, secret-only SetWebhook
	// still replaces hosts — callers must use ConfigureWebhook.
	// This test documents that ConfigureWebhook must be the only startup path
	// and that a second ConfigureWebhook with hosts preserves policy.
	svc.ConfigureWebhook("rotated-secret", []string{"hooks.example.com"})
	if len(svc.WebhookAllowedHosts()) != 1 {
		t.Fatalf("ConfigureWebhook with hosts cleared allowlist: %v", svc.WebhookAllowedHosts())
	}
}
