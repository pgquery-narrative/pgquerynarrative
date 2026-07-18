// Package config provides configuration management for the application.
// It loads configuration from environment variables with sensible defaults.
package config

import (
	"encoding/json"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
)

// Config holds all application configuration.
type Config struct {
	Server    ServerConfig    // HTTP server configuration
	Database  DatabaseConfig  // Database connection settings
	Security  SecurityConfig  // Security settings
	LLM       LLMConfig       // LLM provider configuration
	Metrics   MetricsConfig   // Metrics and period-comparison settings
	Embedding EmbeddingConfig // Optional embeddings for RAG and similar-query retrieval
	Logging   LoggingConfig   // Log level and pretty (colorful) console output
}

// LoggingConfig holds logging behavior. Level is parsed by zerolog (debug, info, warn, error).
// Pretty enables human-readable colorful output for local dev; when false, logs are plain text.
type LoggingConfig struct {
	Level  string // LOG_LEVEL: debug, info, warn, error (default info)
	Pretty bool   // LOG_PRETTY: true = colorful console output
}

// EmbeddingConfig holds settings for embedding models (RAG, similar-query).
// When BaseURL and Model are empty, embeddings are disabled.
type EmbeddingConfig struct {
	BaseURL string // e.g. Ollama base URL (http://localhost:11434)
	Model   string // e.g. nomic-embed-text
}

// MetricsConfig holds settings for metrics and period-over-period comparison.
type MetricsConfig struct {
	// TrendThresholdPercent is the minimum absolute % change to label as "up" or "down" (vs "flat"). Default 0.5.
	TrendThresholdPercent float64
	// AnomalySigma is the z-score threshold for anomaly detection (1–5). Default 2.0.
	AnomalySigma float64
	// AnomalyMethod is the anomaly detection method: "zscore" (default) or "isolation_forest".
	AnomalyMethod string
	// TrendPeriods is the number of periods used for linear regression trend (2–24). Default 6.
	TrendPeriods int
	// MovingAvgWindow is the simple moving average window length (2–24). Default 3.
	MovingAvgWindow int
	// ConfidenceLevel is the confidence level for forecast intervals (e.g. 0.95 for 95%). Clamped to 0.5–0.99.
	ConfidenceLevel float64
	// MinRowsForCorrelation is the minimum rows to compute correlations between numeric measures (2–1000). Default 10.
	MinRowsForCorrelation int
	// SmoothingAlpha is the level smoothing factor for exponential smoothing (0–1). Default 0.3.
	SmoothingAlpha float64
	// SmoothingBeta is the trend smoothing factor for Holt (0–1). Default 0.1.
	SmoothingBeta float64
	// MaxSeasonalLag is the maximum seasonal period to try (2–24). Default 12.
	MaxSeasonalLag int
	// MinPeriodsForSeasonality is the minimum series length to detect seasonality. Default 12.
	MinPeriodsForSeasonality int
	// MaxTimeSeriesPeriods is the maximum number of periods to include in time-series Periods (e.g. "last N" for charts). Default 24. Range 2–120.
	MaxTimeSeriesPeriods int
}

// ServerConfig contains HTTP server settings.
type ServerConfig struct {
	Host            string        // Server host (default: "0.0.0.0")
	Port            int           // Server port (default: 8080)
	ReadTimeout     time.Duration // Request read timeout
	WriteTimeout    time.Duration // Response write timeout
	ShutdownTimeout time.Duration // Graceful shutdown timeout (default: 10s)
	CORSOrigins     []string      // Allowed CORS origins (empty = same-origin only)
}

// DatabaseConfig contains PostgreSQL connection settings.
type DatabaseConfig struct {
	Host             string        // Database host
	Port             int           // Database port
	Database         string        // Database name
	User             string        // Application user (full access)
	Password         string        // Application user password
	MaxConnections   int           // Maximum connection pool size
	MinConnections   int           // Minimum idle connections per pool
	GlobalMaxConns   int           // Maximum total DB connections across app + read-only pools
	ReadOnlyUser     string        // Read-only user (for queries)
	ReadOnlyPassword string        // Read-only user password
	SSLMode          string        // SSL mode (disable, require, etc.)
	QueryTimeout     time.Duration // Maximum query execution time
	LockTimeout      time.Duration // Database-side lock timeout for query connections
	IdleTxTimeout    time.Duration // Database-side idle-in-transaction timeout
	AllowedSchemas   []string      // Schemas queries may access (e.g. demo, public). Default: public,demo.
	MaxResultBytes   int           // Approximate max materialized result bytes
	MaxCellBytes     int           // Approximate max bytes for an individual cell
	MaxColumns       int           // Max columns returned by a query
	Connections      []DataConnectionConfig
	DefaultID        string
}

// DataConnectionConfig is a read-only data source that queries/reports can run against.
type DataConnectionConfig struct {
	ID               string
	Name             string
	Host             string
	Port             int
	Database         string
	ReadOnlyUser     string
	ReadOnlyPassword string
	SSLMode          string
	QueryTimeout     time.Duration
	LockTimeout      time.Duration
	IdleTxTimeout    time.Duration
	AllowedSchemas   []string
	MaxResultBytes   int
	MaxCellBytes     int
	MaxColumns       int
}

// SecurityConfig contains security-related settings.
type SecurityConfig struct {
	AuthEnabled                  bool          // When true, API and web export require Bearer token (SECURITY_API_KEY).
	APIKey                       string        // Bearer token for API auth; required when AuthEnabled is true.
	APIKeyHash                   string        // SHA-256 hex digest of SECURITY_API_KEY (preferred in production).
	RateLimitRPM                 int           // Max requests per minute per client (0 = disabled). Applied when > 0.
	RateLimitBurst               int           // Burst size for rate limiter (allow short spikes). Default 2 * RateLimitRPM when 0.
	TrustedProxies               []string      // IPs/CIDRs that may set X-Forwarded-For / X-Real-IP (empty = use RemoteAddr only).
	MaxRequestBodyBytes          int64         // Max HTTP request body size (0 = default 5 MiB).
	ShareLinkDefaultHours        int           // Default share-link TTL when expires_in_hours is omitted (default 168).
	ShareLinksEnabled            bool          // Allow unauthenticated shared report links. Default false.
	ExplainAnalyzeEnabled        bool          // Allow EXPLAIN ANALYZE (executes query). Default false.
	StatStatementsEnabled        bool          // Expose pg_stat_statements API. Default true.
	APIKeysJSON                  string        // Optional JSON array of {key,id,role} for multi-key RBAC.
	RateLimitDistributed         bool          // Use PostgreSQL-backed rate limiting (multi-replica safe).
	RateLimitFailureMode         string        // Behavior when distributed limiter storage fails: open (default) | closed | local_fallback.
	RateLimitBucketMaxAge        time.Duration // Distributed rate-limit buckets inactive longer than this are cleaned up. Default 24h.
	OIDCIssuer                   string        // Optional OIDC issuer URL for JWT Bearer auth.
	OIDCAudience                 string        // Expected JWT aud claim.
	OIDCJWKSURL                  string        // Optional JWKS URL (defaults to issuer/.well-known/jwks.json).
	ScheduleRunnerEnabled        bool          // Background schedule execution. Default false.
	ScheduleRunnerInterval       time.Duration
	ScheduleDurableLeases        bool   // Require durable schedule_runs claiming. Default true.
	WebhookSigningSecret         string // HMAC secret for outbound webhook signatures.
	OIDCClientID                 string
	OIDCClientSecret             string
	OIDCRedirectURL              string
	SessionSecret                string
	SessionTTL                   time.Duration
	OIDCAutoJoinDefaultOrg       bool     // Auto-provision default-org membership on first OIDC login. Default false.
	WebhookAllowedHosts          []string // Optional hostname allowlist for webhook destinations.
	AuditMode                    string   // Audit durability mode: best_effort (default) | required | buffered.
	ExplainSnapshotRetentionDays int      // Delete stored EXPLAIN snapshots older than this many days (0 = keep forever). Default 90.
	ShareLinkExposeSQL           bool     // When false (default), public shared-report views omit the underlying SQL text.
	DataEncryptionKey            string   // AES-GCM key material for at-rest sealing of EXPLAIN snapshots (SECURITY_DATA_ENCRYPTION_KEY).
}

// LLMConfig contains settings for the LLM provider used for narrative generation.
type LLMConfig struct {
	Provider                    string  // Provider name: "ollama", "gemini", "claude", "openai", "groq"
	Model                       string  // Model name (e.g., "llama3.2", "gpt-4o-mini")
	APIKey                      string  // API key (for cloud providers)
	BaseURL                     string  // Base URL (for local providers like Ollama)
	MaxSampleRows               int     // Max result rows included in narrative prompts (default 5)
	SendRowData                 bool    // When false, prompts omit row values (columns + metrics only)
	AllowExternalData           bool    // Explicit opt-in to send data to non-local LLM providers
	RedactPII                   bool    // Mask sensitive columns and PII patterns in LLM prompts
	DailyTokenBudget            int     // Per-org daily token budget (0 = unlimited)
	DailyCostBudgetUSD          float64 // Per-org daily USD budget (0 = unlimited)
	MonthlyTokenBudget          int     // Per-org monthly token budget (0 = unlimited)
	MonthlyCostBudgetUSD        float64 // Per-org monthly USD budget (0 = unlimited)
	PerUserDailyTokenBudget     int     // Per-user daily token budget (0 = unlimited)
	PerUserDailyCostBudgetUSD   float64 // Per-user daily USD budget (0 = unlimited)
	PerUserMonthlyTokenBudget   int     // Per-user monthly token budget (0 = unlimited)
	PerUserMonthlyCostBudgetUSD float64 // Per-user monthly USD budget (0 = unlimited)
	USDPer1kTokens              float64 // Estimated cost rate for budget accounting
	MaxCallsPerReport           int     // Cap auxiliary LLM calls per report (0 = default 12)
	BudgetFailClosed            bool    // Deny LLM calls when the budget ledger DB is unreachable, instead of failing open
}

// Load reads configuration from environment variables and returns a Config struct.
// All settings have sensible defaults for local development.
func Load() Config {
	cfg := Config{
		Server: ServerConfig{
			Host:        getEnv("PGQUERYNARRATIVE_HOST", "0.0.0.0"),
			Port:        getEnvInt("PGQUERYNARRATIVE_PORT", 8080),
			ReadTimeout: getEnvDuration("PGQUERYNARRATIVE_READ_TIMEOUT", 15*time.Second),
			// Ask / report generation can chain several LLM calls; 60s was too low for local Ollama.
			WriteTimeout:    getEnvDuration("PGQUERYNARRATIVE_WRITE_TIMEOUT", 300*time.Second),
			ShutdownTimeout: getEnvDuration("SHUTDOWN_TIMEOUT", 10*time.Second),
			CORSOrigins:     getEnvSlice("CORS_ORIGINS", ","),
		},
		Database: DatabaseConfig{
			Host:             getEnv("DATABASE_HOST", "localhost"),
			Port:             getEnvInt("DATABASE_PORT", 5432),
			Database:         getEnv("DATABASE_NAME", "pgquerynarrative"),
			User:             getEnv("DATABASE_USER", "pgquerynarrative_app"),
			Password:         getEnv("DATABASE_PASSWORD", "pgquerynarrative_app"),
			MaxConnections:   getEnvInt("DATABASE_MAX_CONNECTIONS", 10),
			MinConnections:   getEnvInt("DATABASE_MIN_CONNECTIONS", 0),
			GlobalMaxConns:   getEnvInt("DATABASE_GLOBAL_MAX_CONNECTIONS", 0),
			ReadOnlyUser:     getEnv("DATABASE_READONLY_USER", "pgquerynarrative_readonly"),
			ReadOnlyPassword: getEnv("DATABASE_READONLY_PASSWORD", "pgquerynarrative_readonly"),
			SSLMode:          getEnv("DATABASE_SSL_MODE", "disable"),
			QueryTimeout:     getEnvDuration("QUERY_TIMEOUT", 30*time.Second),
			LockTimeout:      getEnvDuration("QUERY_LOCK_TIMEOUT", 2*time.Second),
			IdleTxTimeout:    getEnvDuration("QUERY_IDLE_IN_TX_TIMEOUT", 10*time.Second),
			AllowedSchemas:   getEnvAllowedSchemas("DATABASE_ALLOWED_SCHEMAS", "demo,opendata"),
			MaxResultBytes:   getEnvInt("QUERY_MAX_RESULT_BYTES", 10*1024*1024),
			MaxCellBytes:     getEnvInt("QUERY_MAX_CELL_BYTES", 1024*1024),
			MaxColumns:       getEnvInt("QUERY_MAX_COLUMNS", 100),
		},
		Security: SecurityConfig{
			AuthEnabled:                  getEnvBool("SECURITY_AUTH_ENABLED", false),
			APIKey:                       getEnv("SECURITY_API_KEY", ""),
			APIKeyHash:                   getEnv("SECURITY_API_KEY_HASH", ""),
			RateLimitRPM:                 getEnvInt("SECURITY_RATE_LIMIT_RPM", 0),
			RateLimitBurst:               getEnvInt("SECURITY_RATE_LIMIT_BURST", 0),
			TrustedProxies:               getEnvSlice("SECURITY_TRUSTED_PROXIES", ","),
			MaxRequestBodyBytes:          getEnvInt64("SECURITY_MAX_REQUEST_BODY_BYTES", 5*1024*1024),
			ShareLinkDefaultHours:        getEnvInt("SECURITY_SHARE_LINK_DEFAULT_HOURS", 168),
			ShareLinksEnabled:            getEnvBool("SECURITY_SHARE_LINKS_ENABLED", false),
			ExplainAnalyzeEnabled:        getEnvBool("SECURITY_EXPLAIN_ANALYZE_ENABLED", false),
			StatStatementsEnabled:        getEnvBool("SECURITY_STAT_STATEMENTS_ENABLED", true),
			APIKeysJSON:                  auth.LoadAPIKeysJSON(getEnv("SECURITY_API_KEYS_JSON", "")),
			RateLimitDistributed:         getEnvBool("SECURITY_RATE_LIMIT_DISTRIBUTED", false),
			RateLimitFailureMode:         getEnv("SECURITY_RATE_LIMIT_FAILURE_MODE", "open"),
			RateLimitBucketMaxAge:        getEnvDuration("SECURITY_RATE_LIMIT_BUCKET_MAX_AGE", 24*time.Hour),
			OIDCIssuer:                   getEnv("SECURITY_OIDC_ISSUER", ""),
			OIDCAudience:                 getEnv("SECURITY_OIDC_AUDIENCE", ""),
			OIDCJWKSURL:                  getEnv("SECURITY_OIDC_JWKS_URL", ""),
			ScheduleRunnerEnabled:        getEnvBool("SCHEDULE_RUNNER_ENABLED", false),
			ScheduleRunnerInterval:       getEnvDuration("SCHEDULE_RUNNER_INTERVAL", time.Minute),
			ScheduleDurableLeases:        getEnvBool("SCHEDULE_DURABLE_LEASES", true),
			WebhookSigningSecret:         getEnv("SECURITY_WEBHOOK_SIGNING_SECRET", ""),
			OIDCClientID:                 getEnv("SECURITY_OIDC_CLIENT_ID", ""),
			OIDCClientSecret:             getEnv("SECURITY_OIDC_CLIENT_SECRET", ""),
			OIDCRedirectURL:              getEnv("SECURITY_OIDC_REDIRECT_URL", "http://localhost:8080/auth/callback"),
			SessionSecret:                getEnv("SECURITY_SESSION_SECRET", ""),
			SessionTTL:                   getEnvDuration("SECURITY_SESSION_TTL", 8*time.Hour),
			OIDCAutoJoinDefaultOrg:       getEnvBool("SECURITY_OIDC_AUTO_JOIN_DEFAULT_ORG", false),
			WebhookAllowedHosts:          getEnvSlice("SECURITY_WEBHOOK_ALLOWED_HOSTS", ","),
			AuditMode:                    getEnv("SECURITY_AUDIT_MODE", "best_effort"),
			ExplainSnapshotRetentionDays: getEnvInt("SECURITY_EXPLAIN_SNAPSHOT_RETENTION_DAYS", 90),
			ShareLinkExposeSQL:           getEnvBool("SECURITY_SHARE_LINK_EXPOSE_SQL", false),
			DataEncryptionKey:            getEnv("SECURITY_DATA_ENCRYPTION_KEY", ""),
		},
		LLM: LLMConfig{
			Provider:                    getEnv("LLM_PROVIDER", "ollama"),
			Model:                       getEnv("LLM_MODEL", "llama3.2"),
			APIKey:                      getEnv("LLM_API_KEY", ""),
			BaseURL:                     getEnv("LLM_BASE_URL", "http://localhost:11434"),
			MaxSampleRows:               clampInt(getEnvInt("LLM_MAX_SAMPLE_ROWS", 5), 0, 10),
			SendRowData:                 getEnvBool("LLM_SEND_ROW_DATA", false),
			AllowExternalData:           getEnvBool("LLM_ALLOW_EXTERNAL_DATA", false),
			RedactPII:                   getEnvBool("LLM_REDACT_PII", true),
			DailyTokenBudget:            getEnvInt("LLM_DAILY_TOKEN_BUDGET", 0),
			DailyCostBudgetUSD:          getEnvFloat("LLM_DAILY_COST_BUDGET_USD", 0),
			MonthlyTokenBudget:          getEnvInt("LLM_MONTHLY_TOKEN_BUDGET", 0),
			MonthlyCostBudgetUSD:        getEnvFloat("LLM_MONTHLY_COST_BUDGET_USD", 0),
			PerUserDailyTokenBudget:     getEnvInt("LLM_PER_USER_DAILY_TOKEN_BUDGET", 0),
			PerUserDailyCostBudgetUSD:   getEnvFloat("LLM_PER_USER_DAILY_COST_BUDGET_USD", 0),
			PerUserMonthlyTokenBudget:   getEnvInt("LLM_PER_USER_MONTHLY_TOKEN_BUDGET", 0),
			PerUserMonthlyCostBudgetUSD: getEnvFloat("LLM_PER_USER_MONTHLY_COST_BUDGET_USD", 0),
			USDPer1kTokens:              getEnvFloat("LLM_USD_PER_1K_TOKENS", 0.002),
			MaxCallsPerReport:           getEnvInt("LLM_MAX_CALLS_PER_REPORT", 12),
			BudgetFailClosed:            getEnvBool("LLM_BUDGET_FAIL_CLOSED", IsCloudLLMProvider(getEnv("LLM_PROVIDER", "ollama")) || StrictMode()),
		},
		Metrics: validateMetricsConfig(MetricsConfig{
			TrendThresholdPercent:    getEnvFloat("PERIOD_TREND_THRESHOLD_PERCENT", 0.5),
			AnomalySigma:             getEnvFloat("METRICS_ANOMALY_SIGMA", 2.0),
			AnomalyMethod:            getEnv("METRICS_ANOMALY_METHOD", "zscore"),
			TrendPeriods:             getEnvInt("METRICS_TREND_PERIODS", 6),
			MovingAvgWindow:          getEnvInt("METRICS_MOVING_AVG_WINDOW", 3),
			ConfidenceLevel:          getEnvFloat("METRICS_CONFIDENCE_LEVEL", 0.95),
			MinRowsForCorrelation:    getEnvInt("METRICS_CORRELATION_MIN_ROWS", 10),
			SmoothingAlpha:           getEnvFloat("METRICS_SMOOTHING_ALPHA", 0.3),
			SmoothingBeta:            getEnvFloat("METRICS_SMOOTHING_BETA", 0.1),
			MaxSeasonalLag:           getEnvInt("METRICS_MAX_SEASONAL_LAG", 12),
			MinPeriodsForSeasonality: getEnvInt("METRICS_MIN_PERIODS_FOR_SEASONALITY", 12),
			MaxTimeSeriesPeriods:     getEnvInt("METRICS_MAX_TIMESERIES_PERIODS", 24),
		}),
		Embedding: EmbeddingConfig{
			BaseURL: getEnv("EMBEDDING_BASE_URL", ""),
			Model:   getEnv("EMBEDDING_MODEL", "nomic-embed-text"),
		},
		Logging: LoggingConfig{
			Level:  getEnv("LOG_LEVEL", "info"),
			Pretty: getLogPrettyDefault(),
		},
	}
	cfg.Database.DefaultID = getEnv("DATABASE_DEFAULT_CONNECTION_ID", "default")
	cfg.Database.Connections = loadDataConnections(cfg.Database)
	if cfg.Database.DefaultID == "" {
		cfg.Database.DefaultID = "default"
	}
	if len(cfg.Database.Connections) == 0 {
		cfg.Database.Connections = []DataConnectionConfig{defaultDataConnection(cfg.Database)}
	}
	if !hasConnectionID(cfg.Database.Connections, cfg.Database.DefaultID) {
		cfg.Database.DefaultID = cfg.Database.Connections[0].ID
	}
	if cfg.Embedding.BaseURL == "" && cfg.LLM.Provider == "ollama" && cfg.LLM.BaseURL != "" {
		cfg.Embedding.BaseURL = cfg.LLM.BaseURL
	}
	return cfg
}

// validateMetricsConfig clamps metrics config to valid ranges. Call after loading from env.
func validateMetricsConfig(m MetricsConfig) MetricsConfig {
	const (
		minSigma = 1.0
		maxSigma = 5.0
		minWin   = 2
		maxWin   = 24
	)
	if m.AnomalySigma < minSigma {
		m.AnomalySigma = minSigma
	}
	if m.AnomalySigma > maxSigma {
		m.AnomalySigma = maxSigma
	}
	if m.TrendPeriods < minWin {
		m.TrendPeriods = minWin
	}
	if m.TrendPeriods > maxWin {
		m.TrendPeriods = maxWin
	}
	if m.MovingAvgWindow < minWin {
		m.MovingAvgWindow = minWin
	}
	if m.MovingAvgWindow > maxWin {
		m.MovingAvgWindow = maxWin
	}
	if m.ConfidenceLevel < 0.5 {
		m.ConfidenceLevel = 0.5
	}
	if m.ConfidenceLevel > 0.99 {
		m.ConfidenceLevel = 0.99
	}
	if m.MinRowsForCorrelation < 2 {
		m.MinRowsForCorrelation = 2
	}
	if m.MinRowsForCorrelation > 1000 {
		m.MinRowsForCorrelation = 1000
	}
	if m.SmoothingAlpha <= 0 || m.SmoothingAlpha > 1 {
		m.SmoothingAlpha = 0.3
	}
	if m.SmoothingBeta < 0 || m.SmoothingBeta > 1 {
		m.SmoothingBeta = 0.1
	}
	if m.MaxSeasonalLag < 2 {
		m.MaxSeasonalLag = 2
	}
	if m.MaxSeasonalLag > 24 {
		m.MaxSeasonalLag = 24
	}
	if m.MinPeriodsForSeasonality < 2 {
		m.MinPeriodsForSeasonality = 2
	}
	if m.AnomalyMethod != "zscore" && m.AnomalyMethod != "isolation_forest" {
		m.AnomalyMethod = "zscore"
	}
	if m.MaxTimeSeriesPeriods < 2 {
		m.MaxTimeSeriesPeriods = 2
	}
	if m.MaxTimeSeriesPeriods > 120 {
		m.MaxTimeSeriesPeriods = 120
	}
	return m
}

// getEnvFloat retrieves a float environment variable or returns a default value.
func getEnvFloat(key string, fallback float64) float64 {
	if value := os.Getenv(key); value != "" {
		if parsed, err := strconv.ParseFloat(value, 64); err == nil {
			return parsed
		}
	}
	return fallback
}

// getEnv retrieves an environment variable or returns a default value.
func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

// getEnvInt retrieves an integer environment variable or returns a default value.
// If the value cannot be parsed as an integer, the default is returned.
func getEnvInt(key string, fallback int) int {
	if value := os.Getenv(key); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			return parsed
		}
	}
	return fallback
}

func getEnvInt64(key string, fallback int64) int64 {
	if value := os.Getenv(key); value != "" {
		if parsed, err := strconv.ParseInt(value, 10, 64); err == nil {
			return parsed
		}
	}
	return fallback
}

// getEnvBool retrieves a boolean environment variable or returns a default value.
// Accepts: "true", "1", "yes", "on" (case-insensitive) for true; "false", "0", "no", "off" for false.
func getEnvBool(key string, fallback bool) bool {
	if value := os.Getenv(key); value != "" {
		if parsed, err := strconv.ParseBool(value); err == nil {
			return parsed
		}
	}
	return fallback
}

// getLogPrettyDefault returns true when LOG_PRETTY is unset so local runs get readable output;
// when set, uses the env value (e.g. LOG_PRETTY=false for JSON in production).
func getLogPrettyDefault() bool {
	if os.Getenv("LOG_PRETTY") == "" {
		return true
	}
	return getEnvBool("LOG_PRETTY", false)
}

// getEnvDuration retrieves a duration environment variable or returns a default value.
// Accepts formats like "30s", "5m", "1h", etc.
func getEnvDuration(key string, fallback time.Duration) time.Duration {
	if value := os.Getenv(key); value != "" {
		if parsed, err := time.ParseDuration(value); err == nil {
			return parsed
		}
	}
	return fallback
}

// getEnvAllowedSchemas retrieves DATABASE_ALLOWED_SCHEMAS (comma-separated) or returns default (e.g. demo).
func getEnvAllowedSchemas(key, defaultVal string) []string {
	val := os.Getenv(key)
	if val == "" {
		val = defaultVal
	}
	return getEnvSliceWithVal(val, ",")
}

func getEnvSliceWithVal(value, sep string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, sep)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// getEnvSlice retrieves an environment variable, splits by sep, trims each part, and returns non-empty elements.
func getEnvSlice(key, sep string) []string {
	return getEnvSliceWithVal(os.Getenv(key), sep)
}

func defaultDataConnection(db DatabaseConfig) DataConnectionConfig {
	return DataConnectionConfig{
		ID:               "default",
		Name:             "Default",
		Host:             db.Host,
		Port:             db.Port,
		Database:         db.Database,
		ReadOnlyUser:     db.ReadOnlyUser,
		ReadOnlyPassword: db.ReadOnlyPassword,
		SSLMode:          db.SSLMode,
		QueryTimeout:     db.QueryTimeout,
		LockTimeout:      db.LockTimeout,
		IdleTxTimeout:    db.IdleTxTimeout,
		AllowedSchemas:   append([]string(nil), db.AllowedSchemas...),
		MaxResultBytes:   db.MaxResultBytes,
		MaxCellBytes:     db.MaxCellBytes,
		MaxColumns:       db.MaxColumns,
	}
}

func loadDataConnections(db DatabaseConfig) []DataConnectionConfig {
	out := []DataConnectionConfig{defaultDataConnection(db)}
	raw := strings.TrimSpace(os.Getenv("DATABASE_CONNECTIONS_JSON"))
	if raw == "" {
		return out
	}
	var parsed []DataConnectionConfig
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		log.Printf("config: ignoring DATABASE_CONNECTIONS_JSON: %v", err)
		return out
	}
	for _, c := range parsed {
		if strings.TrimSpace(c.ID) == "" {
			continue
		}
		if c.Name == "" {
			c.Name = c.ID
		}
		if c.Host == "" || c.Database == "" || c.ReadOnlyUser == "" {
			continue
		}
		if c.Port <= 0 {
			c.Port = 5432
		}
		if c.SSLMode == "" {
			c.SSLMode = "disable"
		}
		if c.QueryTimeout <= 0 {
			c.QueryTimeout = db.QueryTimeout
		}
		if c.LockTimeout <= 0 {
			c.LockTimeout = db.LockTimeout
		}
		if c.IdleTxTimeout <= 0 {
			c.IdleTxTimeout = db.IdleTxTimeout
		}
		if len(c.AllowedSchemas) == 0 {
			c.AllowedSchemas = append([]string(nil), db.AllowedSchemas...)
		}
		if c.MaxResultBytes <= 0 {
			c.MaxResultBytes = db.MaxResultBytes
		}
		if c.MaxCellBytes <= 0 {
			c.MaxCellBytes = db.MaxCellBytes
		}
		if c.MaxColumns <= 0 {
			c.MaxColumns = db.MaxColumns
		}
		out = append(out, c)
	}
	return out
}

func hasConnectionID(connections []DataConnectionConfig, id string) bool {
	for _, c := range connections {
		if c.ID == id {
			return true
		}
	}
	return false
}

func clampInt(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

// IsCloudLLMProvider reports whether the provider sends prompts to a third-party API.
func IsCloudLLMProvider(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "ollama", "":
		return false
	default:
		return true
	}
}
