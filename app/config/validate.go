package config

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"github.com/pgquerynarrative/pgquerynarrative/app/audit"
	"github.com/pgquerynarrative/pgquerynarrative/app/ratelimit"
)

// Known demo/default passwords and common placeholders rejected in StrictMode.
var defaultPasswords = map[string]struct{}{
	"pgquerynarrative_app":      {},
	"pgquerynarrative_readonly": {},
	"postgres":                  {},
	"changeme":                  {},
	"change-me":                 {},
	"password":                  {},
	"secret":                    {},
	"admin":                     {},
	"root":                      {},
}

const (
	minProductionPasswordLen      = 16
	minProductionSecretLen        = 32
	minProductionWebhookSecretLen = 16
)

// StrictMode returns true when production hardening checks are enforced (APP_ENV=production or SECURITY_STRICT=true).
func StrictMode() bool {
	env := strings.ToLower(strings.TrimSpace(getEnv("APP_ENV", "")))
	if env == "production" || env == "prod" {
		return true
	}
	return getEnvBool("SECURITY_STRICT", false)
}

// Validate checks configuration for production safety. Always validates auth/key pairing
// and forbids open-admin without an explicit insecure opt-in; when StrictMode is enabled,
// also requires TLS, non-default passwords, rate limits, and auth.
func (c Config) Validate() error {
	if c.Security.AuthEnabled && strings.TrimSpace(c.Security.APIKey) == "" && strings.TrimSpace(c.Security.APIKeyHash) == "" && strings.TrimSpace(c.Security.APIKeysJSON) == "" && strings.TrimSpace(c.Security.OIDCIssuer) == "" {
		return fmt.Errorf("SECURITY_API_KEY, SECURITY_API_KEY_HASH, SECURITY_API_KEYS_JSON, or SECURITY_OIDC_ISSUER is required when SECURITY_AUTH_ENABLED=true")
	}
	if !c.Security.AuthEnabled && !c.Security.AllowInsecureNoAuth {
		return fmt.Errorf("SECURITY_AUTH_ENABLED=false requires SECURITY_ALLOW_INSECURE_NO_AUTH=true (explicit opt-in for open-admin local/dev access)")
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
	if !ratelimit.ValidFailureMode(c.Security.RateLimitFailureMode) {
		return fmt.Errorf("SECURITY_RATE_LIMIT_FAILURE_MODE must be one of: open, closed, local_fallback")
	}
	if !audit.ValidMode(c.Security.AuditMode) {
		return fmt.Errorf("SECURITY_AUDIT_MODE must be one of: best_effort, required, buffered")
	}
	if c.Security.AuthEnabled && ratelimit.ParseFailureMode(c.Security.RateLimitFailureMode) == ratelimit.FailOpen {
		return fmt.Errorf("SECURITY_RATE_LIMIT_FAILURE_MODE must not be 'open' when authentication is enabled; use 'closed' or 'local_fallback'")
	}
	if err := validateAllowedSchemas(c.Database.AllowedSchemas, StrictMode()); err != nil {
		return err
	}
	for _, conn := range c.Database.Connections {
		if len(conn.AllowedSchemas) == 0 {
			continue
		}
		if err := validateAllowedSchemas(conn.AllowedSchemas, StrictMode()); err != nil {
			return fmt.Errorf("connection %q: %w", conn.ID, err)
		}
	}
	if !StrictMode() {
		return nil
	}
	if c.Security.AllowInsecureNoAuth {
		return fmt.Errorf("SECURITY_ALLOW_INSECURE_NO_AUTH must be false in production")
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
	if c.Database.IdleTxTimeout <= 0 {
		return fmt.Errorf("QUERY_IDLE_IN_TX_TIMEOUT must be > 0 in production")
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
	if err := requireProductionSSLMode(c.Database.SSLMode); err != nil {
		return err
	}
	if err := requireStrongPassword("DATABASE_PASSWORD", c.Database.Password); err != nil {
		return err
	}
	if err := requireStrongPassword("DATABASE_READONLY_PASSWORD", c.Database.ReadOnlyPassword); err != nil {
		return err
	}
	for _, conn := range c.Database.Connections {
		label := fmt.Sprintf("connection %q readonly password", conn.ID)
		if err := requireStrongPassword(label, conn.ReadOnlyPassword); err != nil {
			return err
		}
		if conn.SSLMode != "" {
			if err := requireProductionSSLMode(conn.SSLMode); err != nil {
				return fmt.Errorf("connection %q: %w", conn.ID, err)
			}
		}
	}
	if c.Security.ExplainAnalyzeEnabled {
		return fmt.Errorf("SECURITY_EXPLAIN_ANALYZE_ENABLED must be false in production")
	}
	if c.Security.ScheduleRunnerEnabled && !c.Security.ScheduleDurableLeases {
		return fmt.Errorf("SCHEDULE_DURABLE_LEASES must be true when SCHEDULE_RUNNER_ENABLED=true in production")
	}
	if c.Security.ScheduleRunnerEnabled {
		if len(c.Security.WebhookAllowedHosts) == 0 {
			return fmt.Errorf("SECURITY_WEBHOOK_ALLOWED_HOSTS is required when SCHEDULE_RUNNER_ENABLED=true in production")
		}
		secret := strings.TrimSpace(c.Security.WebhookSigningSecret)
		if len(secret) < minProductionWebhookSecretLen {
			return fmt.Errorf("SECURITY_WEBHOOK_SIGNING_SECRET must be at least %d characters when SCHEDULE_RUNNER_ENABLED=true in production", minProductionWebhookSecretLen)
		}
		if isWeakSecret(secret) {
			return fmt.Errorf("SECURITY_WEBHOOK_SIGNING_SECRET must not use a placeholder value in production")
		}
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
	if c.Security.OIDCAutoJoinDefaultOrg {
		return fmt.Errorf("SECURITY_OIDC_AUTO_JOIN_DEFAULT_ORG must be false in production; provision memberships explicitly")
	}
	if strings.TrimSpace(c.Security.OIDCIssuer) != "" && strings.TrimSpace(c.Security.OIDCAudience) == "" {
		return fmt.Errorf("SECURITY_OIDC_AUDIENCE is required when OIDC is configured in production")
	}
	if ratelimit.ParseFailureMode(c.Security.RateLimitFailureMode) == ratelimit.FailOpen {
		return fmt.Errorf("SECURITY_RATE_LIMIT_FAILURE_MODE must not be 'open' in production; use 'closed' or 'local_fallback'")
	}
	if audit.ParseMode(c.Security.AuditMode) == audit.ModeBestEffort {
		return fmt.Errorf("SECURITY_AUDIT_MODE must not be 'best_effort' in production; use 'required' or 'buffered'")
	}
	if IsCloudLLMProvider(c.LLM.Provider) && !c.LLM.BudgetFailClosed {
		return fmt.Errorf("LLM_BUDGET_FAIL_CLOSED must be true for cloud LLM providers in production")
	}
	if strings.TrimSpace(c.Security.APIKey) != "" {
		return fmt.Errorf("SECURITY_API_KEY plaintext is not allowed in production; set SECURITY_API_KEY_HASH instead")
	}
	if apiKeysJSONContainsPlaintext(c.Security.APIKeysJSON) {
		return fmt.Errorf("SECURITY_API_KEYS_JSON must use key_hash (not plaintext key) in production")
	}
	encKey := strings.TrimSpace(c.Security.DataEncryptionKey)
	sessionSecret := strings.TrimSpace(c.Security.SessionSecret)
	if encKey == "" && sessionSecret == "" {
		return fmt.Errorf("SECURITY_DATA_ENCRYPTION_KEY (or SECURITY_SESSION_SECRET as fallback) is required in production for EXPLAIN snapshot encryption")
	}
	if encKey != "" {
		if err := requireStrongSecret("SECURITY_DATA_ENCRYPTION_KEY", encKey); err != nil {
			return err
		}
	}
	if sessionSecret != "" {
		if err := requireStrongSecret("SECURITY_SESSION_SECRET", sessionSecret); err != nil {
			return err
		}
	}
	if browserOIDCConfigured(c.Security) && sessionSecret == "" {
		return fmt.Errorf("SECURITY_SESSION_SECRET is required in production when OIDC browser login is configured")
	}
	return nil
}

func browserOIDCConfigured(s SecurityConfig) bool {
	return strings.TrimSpace(s.OIDCIssuer) != "" && strings.TrimSpace(s.OIDCClientID) != ""
}

func requireProductionSSLMode(mode string) error {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "require", "verify-ca", "verify-full":
		return nil
	case "disable", "allow", "prefer", "":
		return fmt.Errorf("DATABASE_SSL_MODE must be require, verify-ca, or verify-full in production (got %q)", mode)
	default:
		return fmt.Errorf("DATABASE_SSL_MODE %q is not allowed in production; use require, verify-ca, or verify-full", mode)
	}
}

func requireStrongPassword(name, password string) error {
	password = strings.TrimSpace(password)
	if password == "" {
		return fmt.Errorf("%s must be set in production", name)
	}
	if len(password) < minProductionPasswordLen {
		return fmt.Errorf("%s must be at least %d characters in production", name, minProductionPasswordLen)
	}
	if isWeakSecret(password) {
		return fmt.Errorf("%s must not use a default or placeholder value in production", name)
	}
	return nil
}

func requireStrongSecret(name, secret string) error {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return fmt.Errorf("%s must be set in production", name)
	}
	if len(secret) < minProductionSecretLen {
		return fmt.Errorf("%s must be at least %d characters in production", name, minProductionSecretLen)
	}
	if isWeakSecret(secret) {
		return fmt.Errorf("%s must not use a placeholder value in production", name)
	}
	return nil
}

// isWeakSecret reports whether s matches a known placeholder or is trivially weak.
func isWeakSecret(s string) bool {
	lower := strings.ToLower(strings.TrimSpace(s))
	if lower == "" {
		return true
	}
	if _, ok := defaultPasswords[lower]; ok {
		return true
	}
	// Helm / docs placeholders and "change me" variants.
	if strings.HasPrefix(lower, "changeme") || strings.HasPrefix(lower, "change-me") || strings.HasPrefix(lower, "replace-me") {
		return true
	}
	if strings.Contains(lower, "replace-with") || strings.Contains(lower, "replace_with") {
		return true
	}
	if isTrivialRepeated(lower) {
		return true
	}
	return false
}

func isTrivialRepeated(s string) bool {
	if len(s) == 0 {
		return true
	}
	first := rune(s[0])
	if !unicode.IsPrint(first) {
		return false
	}
	for _, r := range s {
		if r != first {
			return false
		}
	}
	return true
}

func apiKeysJSONContainsPlaintext(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	var parsed []struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		// Unparseable JSON is rejected elsewhere at load time; treat as unsafe.
		return strings.Contains(raw, `"key"`)
	}
	for _, e := range parsed {
		if strings.TrimSpace(e.Key) != "" {
			return true
		}
	}
	return false
}

// Schemas that must never appear in DATABASE_ALLOWED_SCHEMAS (metadata / catalog blast radius).
var alwaysForbiddenSchemas = map[string]struct{}{
	"app":                {},
	"pg_catalog":         {},
	"information_schema": {},
	"pg_toast":           {},
	"pg_toast_temp_1":    {},
}

// ValidateTenantAllowedSchemas rejects empty or forbidden schema allowlists for tenant DSNs.
func ValidateTenantAllowedSchemas(schemas []string) error {
	if len(schemas) == 0 {
		return fmt.Errorf("allowed_schemas must be non-empty for organisation connection secrets")
	}
	return validateAllowedSchemas(schemas, true)
}

// validateAllowedSchemas rejects empty lists (in production) and forbidden schema names.
func validateAllowedSchemas(schemas []string, strict bool) error {
	if len(schemas) == 0 {
		if strict {
			return fmt.Errorf("DATABASE_ALLOWED_SCHEMAS must be non-empty in production")
		}
		return nil
	}
	for _, s := range schemas {
		name := strings.ToLower(strings.TrimSpace(s))
		if name == "" {
			continue
		}
		if _, bad := alwaysForbiddenSchemas[name]; bad {
			return fmt.Errorf("DATABASE_ALLOWED_SCHEMAS must not include %q (app metadata / system catalogs are not queryable)", s)
		}
		if strict && name == "public" {
			return fmt.Errorf("DATABASE_ALLOWED_SCHEMAS must not include %q in production; curate reporting schemas explicitly", s)
		}
	}
	return nil
}
