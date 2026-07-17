package config

import (
	"fmt"
	"strings"
)

var defaultPasswords = map[string]struct{}{
	"pgquerynarrative_app":      {},
	"pgquerynarrative_readonly": {},
	"postgres":                  {},
}

// StrictMode returns true when production hardening checks are enforced (APP_ENV=production or SECURITY_STRICT=true).
func StrictMode() bool {
	env := strings.ToLower(strings.TrimSpace(getEnv("APP_ENV", "")))
	if env == "production" || env == "prod" {
		return true
	}
	return getEnvBool("SECURITY_STRICT", false)
}

// Validate checks configuration for production safety. Always validates auth/key pairing;
// when StrictMode is enabled, also requires TLS, non-default passwords, rate limits, and auth.
func (c Config) Validate() error {
	if c.Security.AuthEnabled && strings.TrimSpace(c.Security.APIKey) == "" && strings.TrimSpace(c.Security.APIKeyHash) == "" && strings.TrimSpace(c.Security.APIKeysJSON) == "" && strings.TrimSpace(c.Security.OIDCIssuer) == "" {
		return fmt.Errorf("SECURITY_API_KEY, SECURITY_API_KEY_HASH, SECURITY_API_KEYS_JSON, or SECURITY_OIDC_ISSUER is required when SECURITY_AUTH_ENABLED=true")
	}
	if len(c.Security.APIKey) > 0 && len(c.Security.APIKey) < 16 {
		return fmt.Errorf("SECURITY_API_KEY must be at least 16 characters")
	}
	if IsCloudLLMProvider(c.LLM.Provider) && !c.LLM.AllowExternalData {
		return fmt.Errorf("cloud LLM provider %q requires LLM_ALLOW_EXTERNAL_DATA=true", c.LLM.Provider)
	}
	if IsCloudLLMProvider(c.LLM.Provider) && c.LLM.SendRowData && c.LLM.MaxSampleRows > 3 {
		return fmt.Errorf("LLM_MAX_SAMPLE_ROWS must be <= 3 for cloud providers when LLM_SEND_ROW_DATA=true")
	}
	if !StrictMode() {
		return nil
	}
	if !c.Security.AuthEnabled {
		return fmt.Errorf("SECURITY_AUTH_ENABLED must be true in production (APP_ENV=production or SECURITY_STRICT=true)")
	}
	if c.Security.RateLimitRPM <= 0 {
		return fmt.Errorf("SECURITY_RATE_LIMIT_RPM must be > 0 in production")
	}
	if c.Database.QueryTimeout <= 0 {
		return fmt.Errorf("QUERY_TIMEOUT must be > 0 in production")
	}
	if c.Database.LockTimeout <= 0 {
		return fmt.Errorf("QUERY_LOCK_TIMEOUT must be > 0 in production")
	}
	if c.Database.MaxResultBytes <= 0 {
		return fmt.Errorf("QUERY_MAX_RESULT_BYTES must be > 0 in production")
	}
	if c.Database.MaxCellBytes <= 0 {
		return fmt.Errorf("QUERY_MAX_CELL_BYTES must be > 0 in production")
	}
	if c.Database.MaxColumns <= 0 {
		return fmt.Errorf("QUERY_MAX_COLUMNS must be > 0 in production")
	}
	if strings.EqualFold(c.Database.SSLMode, "disable") {
		return fmt.Errorf("DATABASE_SSL_MODE must not be disable in production")
	}
	if _, ok := defaultPasswords[c.Database.Password]; ok {
		return fmt.Errorf("DATABASE_PASSWORD must not use the default value in production")
	}
	if _, ok := defaultPasswords[c.Database.ReadOnlyPassword]; ok {
		return fmt.Errorf("DATABASE_READONLY_PASSWORD must not use the default value in production")
	}
	for _, conn := range c.Database.Connections {
		if _, ok := defaultPasswords[conn.ReadOnlyPassword]; ok {
			return fmt.Errorf("connection %q uses a default readonly password", conn.ID)
		}
	}
	if c.Security.ExplainAnalyzeEnabled {
		return fmt.Errorf("SECURITY_EXPLAIN_ANALYZE_ENABLED must be false in production")
	}
	if c.Security.ScheduleRunnerEnabled && !c.Security.ScheduleDurableLeases {
		return fmt.Errorf("SCHEDULE_DURABLE_LEASES must be true when SCHEDULE_RUNNER_ENABLED=true in production")
	}
	if c.Security.ShareLinksEnabled {
		return fmt.Errorf("SECURITY_SHARE_LINKS_ENABLED must be false in production until public sharing is hardened")
	}
	if !c.Security.RateLimitDistributed && c.Security.RateLimitRPM > 0 {
		return fmt.Errorf("SECURITY_RATE_LIMIT_DISTRIBUTED must be true in production when rate limiting is enabled")
	}
	if !c.LLM.SendRowData && c.LLM.MaxSampleRows > 0 {
		// ok: row data disabled
	} else if c.LLM.MaxSampleRows > 5 {
		return fmt.Errorf("LLM_MAX_SAMPLE_ROWS must be <= 5 in production")
	}
	if !c.LLM.RedactPII && c.LLM.SendRowData && IsCloudLLMProvider(c.LLM.Provider) {
		return fmt.Errorf("LLM_REDACT_PII must be true in production when sending row data to cloud LLM providers")
	}
	return nil
}
