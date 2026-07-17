// Package narrative provides a reusable client for PgQueryNarrative capabilities:
// running queries, generating reports, and exposing Goa service implementations
// for use by the standalone server or embedded applications.
package narrative

import (
	"fmt"
	"time"

	"github.com/pgquerynarrative/pgquerynarrative/app/config"
)

// Config holds configuration for the narrative client. It can be built from
// environment (via app/config.Load) or supplied in code for library usage.
type Config struct {
	// Database holds PostgreSQL connection settings for both read-only and app pools.
	Database DatabaseConfig
	// LLM holds the LLM provider settings for narrative generation.
	LLM LLMConfig
	// Metrics holds trend threshold for period comparison (e.g. 0.5 for 0.5%).
	Metrics MetricsConfig
	// Embedding holds optional settings for RAG and similar-query retrieval. When BaseURL is empty, embeddings are disabled.
	Embedding EmbeddingConfig
	// AllowedSchemas is the list of schema names queries may access (e.g. []string{"demo"}).
	AllowedSchemas []string
	// MaxQueryLength is the maximum allowed query length in bytes.
	MaxQueryLength int
	// MaxRowsPerQuery is the maximum rows returned per query execution.
	MaxRowsPerQuery int
	// Security holds security-related settings propagated from app config.
	Security SecurityConfig
}

// SecurityConfig holds security settings for narrative client services.
type SecurityConfig struct {
	AuthEnabled           bool
	RateLimitRPM          int
	RateLimitBurst        int
	RateLimitDistributed  bool
	OIDCIssuer            string
	OIDCAudience          string
	OIDCClientID          string
	OIDCClientSecret      string
	OIDCRedirectURL       string
	SessionSecret         string
	SessionTTL            time.Duration
	ShareLinkDefaultHours int
	ShareLinksEnabled     bool
	ExplainAnalyzeEnabled bool
	StatStatementsEnabled bool
	WebhookSigningSecret  string
	WebhookAllowedHosts   []string
}

// EmbeddingConfig holds optional embedding model settings (e.g. Ollama nomic-embed-text).
type EmbeddingConfig struct {
	BaseURL string
	Model   string
}

// DatabaseConfig holds PostgreSQL connection settings.
type DatabaseConfig struct {
	Host             string
	Port             int
	Database         string
	User             string
	Password         string
	MaxConnections   int
	MinConnections   int
	GlobalMaxConns   int
	ReadOnlyUser     string
	ReadOnlyPassword string
	SSLMode          string
	QueryTimeout     time.Duration
	LockTimeout      time.Duration
	IdleTxTimeout    time.Duration
	DefaultID        string
	Connections      []DataConnectionConfig
}

// DataConnectionConfig defines one read-only data source.
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

// LLMConfig holds LLM provider settings.
type LLMConfig struct {
	Provider                    string
	Model                       string
	APIKey                      string
	BaseURL                     string
	MaxSampleRows               int
	SendRowData                 bool
	AllowExternalData           bool
	RedactPII                   bool
	DailyTokenBudget            int
	DailyCostBudgetUSD          float64
	MonthlyTokenBudget          int
	MonthlyCostBudgetUSD        float64
	PerUserDailyTokenBudget     int
	PerUserDailyCostBudgetUSD   float64
	PerUserMonthlyTokenBudget   int
	PerUserMonthlyCostBudgetUSD float64
	USDPer1kTokens              float64
	MaxCallsPerReport           int
}

// MetricsConfig holds metrics and period-comparison settings.
type MetricsConfig struct {
	TrendThresholdPercent    float64
	AnomalySigma             float64 // Z-score threshold for anomaly detection (1–5)
	AnomalyMethod            string  // "zscore" or "isolation_forest"
	TrendPeriods             int     // Periods for linear regression trend (2–24)
	MovingAvgWindow          int     // Moving average window length (2–24)
	ConfidenceLevel          float64 // Confidence level for forecast intervals (e.g. 0.95)
	MinRowsForCorrelation    int     // Min rows to compute correlations (default 10)
	SmoothingAlpha           float64 // Level smoothing for exponential smoothing (default 0.3)
	SmoothingBeta            float64 // Trend smoothing for Holt (default 0.1)
	MaxSeasonalLag           int     // Max seasonal period to try (default 12)
	MinPeriodsForSeasonality int     // Min series length for seasonality (default 12)
	MaxTimeSeriesPeriods     int     // Max periods returned for time-series metrics (default 24)
}

// FromAppConfig converts app config into narrative config with default
// allowed schemas and limits. Use this when building a client from
// config.Load() in the standalone server.
func FromAppConfig(cfg config.Config) Config {
	return Config{
		Database: DatabaseConfig{
			Host:             cfg.Database.Host,
			Port:             cfg.Database.Port,
			Database:         cfg.Database.Database,
			User:             cfg.Database.User,
			Password:         cfg.Database.Password,
			MaxConnections:   cfg.Database.MaxConnections,
			MinConnections:   cfg.Database.MinConnections,
			GlobalMaxConns:   cfg.Database.GlobalMaxConns,
			ReadOnlyUser:     cfg.Database.ReadOnlyUser,
			ReadOnlyPassword: cfg.Database.ReadOnlyPassword,
			SSLMode:          cfg.Database.SSLMode,
			QueryTimeout:     cfg.Database.QueryTimeout,
			LockTimeout:      cfg.Database.LockTimeout,
			IdleTxTimeout:    cfg.Database.IdleTxTimeout,
			DefaultID:        cfg.Database.DefaultID,
			Connections:      toNarrativeConnections(cfg.Database.Connections),
		},
		LLM: LLMConfig{
			Provider:                    cfg.LLM.Provider,
			Model:                       cfg.LLM.Model,
			APIKey:                      cfg.LLM.APIKey,
			BaseURL:                     cfg.LLM.BaseURL,
			MaxSampleRows:               cfg.LLM.MaxSampleRows,
			SendRowData:                 cfg.LLM.SendRowData,
			AllowExternalData:           cfg.LLM.AllowExternalData,
			RedactPII:                   cfg.LLM.RedactPII,
			DailyTokenBudget:            cfg.LLM.DailyTokenBudget,
			DailyCostBudgetUSD:          cfg.LLM.DailyCostBudgetUSD,
			MonthlyTokenBudget:          cfg.LLM.MonthlyTokenBudget,
			MonthlyCostBudgetUSD:        cfg.LLM.MonthlyCostBudgetUSD,
			PerUserDailyTokenBudget:     cfg.LLM.PerUserDailyTokenBudget,
			PerUserDailyCostBudgetUSD:   cfg.LLM.PerUserDailyCostBudgetUSD,
			PerUserMonthlyTokenBudget:   cfg.LLM.PerUserMonthlyTokenBudget,
			PerUserMonthlyCostBudgetUSD: cfg.LLM.PerUserMonthlyCostBudgetUSD,
			USDPer1kTokens:              cfg.LLM.USDPer1kTokens,
			MaxCallsPerReport:           cfg.LLM.MaxCallsPerReport,
		},
		Metrics: MetricsConfig{
			TrendThresholdPercent:    cfg.Metrics.TrendThresholdPercent,
			AnomalySigma:             cfg.Metrics.AnomalySigma,
			AnomalyMethod:            cfg.Metrics.AnomalyMethod,
			TrendPeriods:             cfg.Metrics.TrendPeriods,
			MovingAvgWindow:          cfg.Metrics.MovingAvgWindow,
			ConfidenceLevel:          cfg.Metrics.ConfidenceLevel,
			MinRowsForCorrelation:    cfg.Metrics.MinRowsForCorrelation,
			SmoothingAlpha:           cfg.Metrics.SmoothingAlpha,
			SmoothingBeta:            cfg.Metrics.SmoothingBeta,
			MaxSeasonalLag:           cfg.Metrics.MaxSeasonalLag,
			MinPeriodsForSeasonality: cfg.Metrics.MinPeriodsForSeasonality,
			MaxTimeSeriesPeriods:     cfg.Metrics.MaxTimeSeriesPeriods,
		},
		Embedding: EmbeddingConfig{
			BaseURL: cfg.Embedding.BaseURL,
			Model:   cfg.Embedding.Model,
		},
		AllowedSchemas:  allowedSchemasOrDefault(cfg.Database.AllowedSchemas),
		MaxQueryLength:  10000,
		MaxRowsPerQuery: 1000,
		Security: SecurityConfig{
			AuthEnabled:           cfg.Security.AuthEnabled,
			RateLimitRPM:          cfg.Security.RateLimitRPM,
			RateLimitBurst:        cfg.Security.RateLimitBurst,
			RateLimitDistributed:  cfg.Security.RateLimitDistributed,
			OIDCIssuer:            cfg.Security.OIDCIssuer,
			OIDCAudience:          cfg.Security.OIDCAudience,
			OIDCClientID:          cfg.Security.OIDCClientID,
			OIDCClientSecret:      cfg.Security.OIDCClientSecret,
			OIDCRedirectURL:       cfg.Security.OIDCRedirectURL,
			SessionSecret:         cfg.Security.SessionSecret,
			SessionTTL:            cfg.Security.SessionTTL,
			ShareLinkDefaultHours: cfg.Security.ShareLinkDefaultHours,
			ShareLinksEnabled:     cfg.Security.ShareLinksEnabled,
			ExplainAnalyzeEnabled: cfg.Security.ExplainAnalyzeEnabled,
			StatStatementsEnabled: cfg.Security.StatStatementsEnabled,
			WebhookSigningSecret:  cfg.Security.WebhookSigningSecret,
			WebhookAllowedHosts:   append([]string(nil), cfg.Security.WebhookAllowedHosts...),
		},
	}
}

func allowedSchemasOrDefault(schemas []string) []string {
	if len(schemas) > 0 {
		return schemas
	}
	return []string{"demo"}
}

func toNarrativeConnections(in []config.DataConnectionConfig) []DataConnectionConfig {
	out := make([]DataConnectionConfig, 0, len(in))
	for _, c := range in {
		out = append(out, DataConnectionConfig{
			ID:               c.ID,
			Name:             c.Name,
			Host:             c.Host,
			Port:             c.Port,
			Database:         c.Database,
			ReadOnlyUser:     c.ReadOnlyUser,
			ReadOnlyPassword: c.ReadOnlyPassword,
			SSLMode:          c.SSLMode,
			QueryTimeout:     c.QueryTimeout,
			LockTimeout:      c.LockTimeout,
			IdleTxTimeout:    c.IdleTxTimeout,
			AllowedSchemas:   append([]string(nil), c.AllowedSchemas...),
			MaxResultBytes:   c.MaxResultBytes,
			MaxCellBytes:     c.MaxCellBytes,
			MaxColumns:       c.MaxColumns,
		})
	}
	return out
}

// Validate applies cloud LLM and production safety checks for library consumers.
func (c Config) Validate() error {
	if config.IsCloudLLMProvider(c.LLM.Provider) && !c.LLM.AllowExternalData {
		return fmt.Errorf("cloud LLM provider %q requires LLM_ALLOW_EXTERNAL_DATA=true", c.LLM.Provider)
	}
	if config.IsCloudLLMProvider(c.LLM.Provider) && c.LLM.SendRowData && c.LLM.MaxSampleRows > 3 {
		return fmt.Errorf("LLM_MAX_SAMPLE_ROWS must be <= 3 for cloud providers when LLM_SEND_ROW_DATA=true")
	}
	if !config.StrictMode() {
		return nil
	}
	ac := config.Config{
		Database: config.DatabaseConfig{
			SSLMode:          c.Database.SSLMode,
			QueryTimeout:     c.Database.QueryTimeout,
			LockTimeout:      c.Database.LockTimeout,
			Password:         c.Database.Password,
			ReadOnlyPassword: c.Database.ReadOnlyPassword,
			Connections:      toAppConnectionsFromNarrative(c.Database.Connections),
		},
		Security: config.SecurityConfig{
			AuthEnabled:           c.Security.AuthEnabled,
			RateLimitRPM:          c.Security.RateLimitRPM,
			RateLimitBurst:        c.Security.RateLimitBurst,
			RateLimitDistributed:  c.Security.RateLimitDistributed,
			ExplainAnalyzeEnabled: c.Security.ExplainAnalyzeEnabled,
			ShareLinksEnabled:     c.Security.ShareLinksEnabled,
			ScheduleDurableLeases: true,
		},
		LLM: config.LLMConfig{
			Provider:          c.LLM.Provider,
			SendRowData:       c.LLM.SendRowData,
			AllowExternalData: c.LLM.AllowExternalData,
			MaxSampleRows:     c.LLM.MaxSampleRows,
			RedactPII:         c.LLM.RedactPII,
		},
	}
	return ac.Validate()
}

func toAppConnectionsFromNarrative(in []DataConnectionConfig) []config.DataConnectionConfig {
	out := make([]config.DataConnectionConfig, 0, len(in))
	for _, c := range in {
		out = append(out, config.DataConnectionConfig{
			ID:               c.ID,
			ReadOnlyPassword: c.ReadOnlyPassword,
		})
	}
	return out
}
