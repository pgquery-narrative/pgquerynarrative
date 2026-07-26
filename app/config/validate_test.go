package config

import (
	"testing"
	"time"
)

func TestValidate_AuthRequiresKey(t *testing.T) {
	cfg := Config{Security: SecurityConfig{AuthEnabled: true, APIKey: ""}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error when auth enabled without API key")
	}
}

func TestValidate_AuthDisabledRequiresInsecureOptIn(t *testing.T) {
	cfg := Config{Security: SecurityConfig{AuthEnabled: false, AllowInsecureNoAuth: false}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error when auth disabled without insecure opt-in")
	}
	cfg.Security.AllowInsecureNoAuth = true
	if err := cfg.Validate(); err != nil {
		t.Fatalf("insecure opt-in should allow auth-disabled in non-strict mode: %v", err)
	}
}

func TestValidate_ShortAPIKey(t *testing.T) {
	cfg := Config{Security: SecurityConfig{AuthEnabled: true, APIKey: "short"}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for short API key")
	}
}

func TestValidate_StrictMode(t *testing.T) {
	t.Setenv("APP_ENV", "production")

	cfg := Load()
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected strict validation error with default dev config")
	}
}

func TestValidate_DevModePasses(t *testing.T) {
	t.Setenv("APP_ENV", "")
	t.Setenv("SECURITY_STRICT", "")
	t.Setenv("SECURITY_ALLOW_INSECURE_NO_AUTH", "true")

	cfg := Load()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("dev config should validate: %v", err)
	}
}

func validProductionConfig() Config {
	return Config{
		Database: DatabaseConfig{
			Password:         "prod-app-password-ok!",
			ReadOnlyPassword: "prod-ro-password-ok!",
			SSLMode:          "require",
			QueryTimeout:     30 * time.Second,
			LockTimeout:      2 * time.Second,
			IdleTxTimeout:    10 * time.Second,
			MaxResultBytes:   10 * 1024 * 1024,
			MaxCellBytes:     1024 * 1024,
			MaxColumns:       100,
			AllowedSchemas:   []string{"analytics"},
		},
		Security: SecurityConfig{
			AuthEnabled:           true,
			AllowInsecureNoAuth:   false,
			APIKeyHash:            "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			RateLimitRPM:          120,
			RateLimitDistributed:  true,
			RateLimitFailureMode:  "closed",
			AuditMode:             "required",
			ExplainAnalyzeEnabled: false,
			ShareLinksEnabled:     false,
			ScheduleRunnerEnabled: false,
			ScheduleDurableLeases: true,
			SessionSecret:         "session-secret-at-least-thirty-two-chars!",
			DataEncryptionKey:     "encryption-key-at-least-thirty-two-ch!",
		},
		LLM: LLMConfig{
			Provider:          "ollama",
			SendRowData:       false,
			AllowExternalData: false,
			MaxSampleRows:     5,
			RedactPII:         true,
			BudgetFailClosed:  true,
		},
	}
}

func TestValidate_StrictModeAcceptsHardenedConfig(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	cfg := validProductionConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("hardened production config should validate: %v", err)
	}
}

func TestValidate_StrictModeRejectsInsecureNoAuth(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	cfg := validProductionConfig()
	cfg.Security.AllowInsecureNoAuth = true
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for insecure no-auth in production")
	}
}

func TestValidate_StrictModeRejectsChangemePassword(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	cfg := validProductionConfig()
	cfg.Database.Password = "changeme"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for changeme database password")
	}
}

func TestValidate_StrictModeRejectsPlaceholderAPIKeyMaterial(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	cfg := validProductionConfig()
	cfg.Security.SessionSecret = "changeme-replace-with-long-random-session-secret"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for placeholder session secret")
	}
}

func TestValidate_StrictModeRejectsWeakSSL(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	for _, mode := range []string{"disable", "allow", "prefer", ""} {
		cfg := validProductionConfig()
		cfg.Database.SSLMode = mode
		if err := cfg.Validate(); err == nil {
			t.Fatalf("expected error for SSL mode %q", mode)
		}
	}
}

func TestValidate_StrictModeRejectsFailOpenRateLimit(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	cfg := validProductionConfig()
	cfg.Security.RateLimitFailureMode = "open"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for fail-open rate limit")
	}
}

func TestValidate_StrictModeRejectsBestEffortAudit(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	cfg := validProductionConfig()
	cfg.Security.AuditMode = "best_effort"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for best_effort audit")
	}
}

func TestValidate_StrictModeScheduleRequiresWebhookAllowlist(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	cfg := validProductionConfig()
	cfg.Security.ScheduleRunnerEnabled = true
	cfg.Security.ScheduleDurableLeases = true
	cfg.Security.WebhookSigningSecret = "webhook-signing-secret-ok"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error when schedule runner enabled without webhook allowlist")
	}
	cfg.Security.WebhookAllowedHosts = []string{"hooks.example.com"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("schedule runner with allowlist+secret should validate: %v", err)
	}
}

func TestValidate_StrictModeRejectsPlaintextAPIKey(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	cfg := validProductionConfig()
	cfg.Security.APIKeyHash = ""
	cfg.Security.APIKey = "plaintext-api-key-long-enough"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for plaintext API key in production")
	}
}

func TestValidate_StrictModeRejectsEmptyAllowedSchemas(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	cfg := validProductionConfig()
	cfg.Database.AllowedSchemas = nil
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for empty allowed schemas")
	}
}

func TestValidate_RejectsAppSchemaAllowlist(t *testing.T) {
	cfg := Config{
		Security: SecurityConfig{AuthEnabled: false, AllowInsecureNoAuth: true},
		Database: DatabaseConfig{AllowedSchemas: []string{"demo", "app"}},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error when app schema is allowlisted")
	}
}

func TestValidate_StrictModeRejectsPublicSchema(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	cfg := validProductionConfig()
	cfg.Database.AllowedSchemas = []string{"public"}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for public schema in production allowlist")
	}
}

func TestIsWeakSecret(t *testing.T) {
	weak := []string{"changeme", "changeme-foo", "change-me-bar", "aaaaaaaaaaaaaaaa", "password"}
	for _, s := range weak {
		if !isWeakSecret(s) {
			t.Fatalf("expected %q to be weak", s)
		}
	}
	if isWeakSecret("prod-app-password-ok!") {
		t.Fatal("strong password marked weak")
	}
}
