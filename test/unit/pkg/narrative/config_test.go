package narrative_test

import (
	"testing"
	"time"

	appconfig "github.com/pgquerynarrative/pgquerynarrative/app/config"
	"github.com/pgquerynarrative/pgquerynarrative/pkg/narrative"
)

func TestFromAppConfig_MapsSecurityFields(t *testing.T) {
	ac := appconfig.Config{
		Security: appconfig.SecurityConfig{
			AuthEnabled:          true,
			RateLimitRPM:         120,
			RateLimitBurst:       240,
			RateLimitDistributed: true,
			OIDCIssuer:           "https://idp.example.com",
			OIDCAudience:         "pgquerynarrative",
			OIDCClientID:         "client-id",
			OIDCClientSecret:     "secret",
			OIDCRedirectURL:      "http://localhost:8080/auth/callback",
			SessionSecret:        "session-secret",
			SessionTTL:           8 * time.Hour,
			WebhookSigningSecret: "webhook-secret",
			WebhookAllowedHosts:  []string{"hooks.example.com"},
		},
	}
	cfg := narrative.FromAppConfig(ac)
	if !cfg.Security.AuthEnabled {
		t.Fatal("AuthEnabled not mapped")
	}
	if cfg.Security.RateLimitRPM != 120 || cfg.Security.RateLimitBurst != 240 {
		t.Fatalf("rate limit not mapped: rpm=%d burst=%d", cfg.Security.RateLimitRPM, cfg.Security.RateLimitBurst)
	}
	if cfg.Security.OIDCIssuer != "https://idp.example.com" || cfg.Security.OIDCClientID != "client-id" {
		t.Fatalf("OIDC not mapped: %+v", cfg.Security)
	}
	if cfg.Security.SessionTTL != 8*time.Hour {
		t.Fatalf("SessionTTL not mapped: %v", cfg.Security.SessionTTL)
	}
	if cfg.Security.WebhookSigningSecret != "webhook-secret" {
		t.Fatalf("signing secret not mapped")
	}
	if len(cfg.Security.WebhookAllowedHosts) != 1 || cfg.Security.WebhookAllowedHosts[0] != "hooks.example.com" {
		t.Fatalf("allowlist not mapped: %v", cfg.Security.WebhookAllowedHosts)
	}
}

func TestFromAppConfig_MapsMetricsFields(t *testing.T) {
	ac := appconfig.Config{
		Metrics: appconfig.MetricsConfig{
			TrendThresholdPercent:    1.5,
			AnomalySigma:             2.5,
			AnomalyMethod:            "isolation_forest",
			TrendPeriods:             6,
			MovingAvgWindow:          4,
			ConfidenceLevel:          0.9,
			MinRowsForCorrelation:    15,
			SmoothingAlpha:           0.4,
			SmoothingBeta:            0.2,
			MaxSeasonalLag:           8,
			MinPeriodsForSeasonality: 10,
			MaxTimeSeriesPeriods:     48,
		},
	}
	cfg := narrative.FromAppConfig(ac)
	if cfg.Metrics.MaxTimeSeriesPeriods != 48 {
		t.Fatalf("MaxTimeSeriesPeriods not mapped: got %d", cfg.Metrics.MaxTimeSeriesPeriods)
	}
	if cfg.Metrics.TrendThresholdPercent != 1.5 || cfg.Metrics.AnomalyMethod != "isolation_forest" {
		t.Fatalf("metrics fields not mapped: %+v", cfg.Metrics)
	}
}

func TestConfig_Validate_CloudLLMRequiresOptIn(t *testing.T) {
	cfg := narrative.Config{
		Security: narrative.SecurityConfig{AllowInsecureNoAuth: true},
		LLM:      narrative.LLMConfig{Provider: "openai", AllowExternalData: false},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected cloud LLM validation error")
	}
}

func TestConfig_Validate_AuthDisabledRequiresInsecureOptIn(t *testing.T) {
	cfg := narrative.Config{
		Security: narrative.SecurityConfig{AuthEnabled: false, AllowInsecureNoAuth: false},
		LLM:      narrative.LLMConfig{Provider: "ollama"},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected insecure opt-in validation error")
	}
}
