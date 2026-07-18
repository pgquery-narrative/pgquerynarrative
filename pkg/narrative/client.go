package narrative

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgquerynarrative/pgquerynarrative/api/gen/queries"
	"github.com/pgquerynarrative/pgquerynarrative/api/gen/reports"
	schema "github.com/pgquerynarrative/pgquerynarrative/api/gen/schema"
	suggestions "github.com/pgquerynarrative/pgquerynarrative/api/gen/suggestions"
	"github.com/pgquerynarrative/pgquerynarrative/app/audit"
	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/app/catalog"
	appconfig "github.com/pgquerynarrative/pgquerynarrative/app/config"
	"github.com/pgquerynarrative/pgquerynarrative/app/db"
	"github.com/pgquerynarrative/pgquerynarrative/app/embedding"
	"github.com/pgquerynarrative/pgquerynarrative/app/llm"
	"github.com/pgquerynarrative/pgquerynarrative/app/queryrunner"
	"github.com/pgquerynarrative/pgquerynarrative/app/service"
	pkgsuggestions "github.com/pgquerynarrative/pgquerynarrative/app/suggestions"
	"github.com/pgquerynarrative/pgquerynarrative/gen/connections"
	"github.com/pgquerynarrative/pgquerynarrative/gen/dashboards"
	"github.com/pgquerynarrative/pgquerynarrative/gen/schedules"
)

// Client provides access to narrative capabilities: running queries, generating
// reports, schema discovery, saved queries, and the Goa service implementations
// for HTTP or embedded use. All methods accept context.Context; cancellation
// is propagated to the underlying operations. Call Close when done to release
// database connection pools; Close is idempotent and safe to call multiple times.
type Client struct {
	pools              *db.Pools
	queriesService     *service.QueriesService
	reportsService     *service.ReportsService
	dashboardsService  *service.DashboardsService
	schedulesService   *service.SchedulesService
	schemaService      *service.SchemaService
	connectionsService *service.ConnectionsService
	suggestionsService suggestions.Service
	budgetStore        *llm.BudgetStore
	auditStore         *audit.Store
}

// NewClient builds a narrative client from the given config. It creates
// database pools, query runner, LLM client, and all services. The returned
// client must be closed to release resources.
//
// NewClient is the sole wiring point for security-sensitive service
// configuration: connection authorization (SetAuthorizer /
// service.ConnectionAuthorizer), webhook signing/allowlisting
// (SchedulesService.ConfigureWebhook), and LLM governance
// (SetLLMGovernance / service.GovernedAI). Callers (cmd/server/main.go,
// tests, embedders) must treat the services returned by Client's accessors
// as configured-and-frozen: do not call SetAuthorizer, ConfigureWebhook,
// SetLLMGovernance, or other construction-time Set*/Configure* security
// setters again after NewClient returns. Methods that only toggle runtime
// behavior already exposed to end users (e.g. per-request options) are not
// subject to this restriction.
func NewClient(ctx context.Context, cfg Config) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	dbCfg := appconfig.DatabaseConfig{
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
		Connections:      toAppConnections(cfg.Database.Connections),
	}
	pools, err := db.NewPools(ctx, dbCfg)
	if err != nil {
		return nil, err
	}

	allowedSchemas := cfg.AllowedSchemas
	if len(allowedSchemas) == 0 {
		allowedSchemas = []string{"demo"}
	}
	maxQueryLength := cfg.MaxQueryLength
	if maxQueryLength <= 0 {
		maxQueryLength = 10000
	}
	maxRows := cfg.MaxRowsPerQuery
	if maxRows <= 0 {
		maxRows = queryrunner.DefaultMaxRows
	}

	validator := queryrunner.NewValidator(allowedSchemas, maxQueryLength)
	llmClient := newLLMClient(LLMConfig(cfg.LLM))
	defaultConnectionID := pools.DefaultConnectionID

	runners := make(map[string]*queryrunner.Runner, len(dbCfg.Connections))
	loaders := make(map[string]*catalog.Loader, len(dbCfg.Connections))
	readonlyUsers := make(map[string]string, len(dbCfg.Connections))
	connectionItems := make([]*connections.ConnectionInfo, 0, len(dbCfg.Connections))
	for _, conn := range dbCfg.Connections {
		connSchemas := conn.AllowedSchemas
		if len(connSchemas) == 0 {
			connSchemas = allowedSchemas
		}
		connValidator := queryrunner.NewValidator(connSchemas, maxQueryLength)
		timeout := conn.QueryTimeout
		if timeout <= 0 {
			timeout = cfg.Database.QueryTimeout
		}
		runners[conn.ID] = queryrunner.NewRunnerForConnection(
			pools,
			conn.ID,
			connValidator,
			maxRows,
			timeout,
			queryrunner.WithExplainAnalyze(cfg.Security.ExplainAnalyzeEnabled),
			queryrunner.WithResultLimits(conn.MaxResultBytes, conn.MaxCellBytes, conn.MaxColumns),
		)
		loaders[conn.ID] = catalog.NewLoaderForConnection(pools, conn.ID, connSchemas)
		readonlyUsers[conn.ID] = conn.ReadOnlyUser
		connectionItems = append(connectionItems, &connections.ConnectionInfo{ID: conn.ID, Name: conn.Name})
	}

	promptOpts := llm.PromptOptions{
		MaxSampleRows: cfg.LLM.MaxSampleRows,
		SendRowData:   cfg.LLM.SendRowData,
		RedactPII:     cfg.LLM.RedactPII,
	}

	appDB := db.NewOrgScoped(pools.App)
	llmAuditStore := llm.NewAuditStore(pools.App)
	secAuditStore := audit.NewStore(pools.App, audit.ParseMode(cfg.Security.AuditMode))
	budgetStore := llm.NewBudgetStore(pools.App, llm.BudgetConfig{
		DailyTokenLimit:          cfg.LLM.DailyTokenBudget,
		DailyCostUSD:             cfg.LLM.DailyCostBudgetUSD,
		MonthlyTokenLimit:        cfg.LLM.MonthlyTokenBudget,
		MonthlyCostUSD:           cfg.LLM.MonthlyCostBudgetUSD,
		PerUserDailyTokenLimit:   cfg.LLM.PerUserDailyTokenBudget,
		PerUserDailyCostUSD:      cfg.LLM.PerUserDailyCostBudgetUSD,
		PerUserMonthlyTokenLimit: cfg.LLM.PerUserMonthlyTokenBudget,
		PerUserMonthlyCostUSD:    cfg.LLM.PerUserMonthlyCostBudgetUSD,
		USDPer1kTokens:           cfg.LLM.USDPer1kTokens,
		FailClosed:               cfg.LLM.BudgetFailClosed,
	})

	var queriesService *service.QueriesService
	var reportsService *service.ReportsService
	var suggester *pkgsuggestions.Suggester
	embeddingStore := embedding.NewStore(appDB)

	metricsCfg := appconfig.MetricsConfig{
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
	}
	if cfg.Embedding.BaseURL != "" && cfg.Embedding.Model != "" {
		rawEmbedder := embedding.NewOllamaEmbedder(cfg.Embedding.BaseURL, cfg.Embedding.Model)
		// Ollama embeddings run against a local/self-hosted server, so "ollama" is the
		// provider label for audit/policy purposes; a future cloud embedding provider
		// would use its own provider name here so config.IsCloudLLMProvider gates it.
		govEmb := embedding.NewGovernedEmbedder(rawEmbedder, llmAuditStore, "ollama", cfg.LLM.AllowExternalData, cfg.LLM.RedactPII)
		govEmb.SetExpectedDimension(embedding.EmbeddingVectorDimension)
		emb := embedding.Embedder(govEmb)
		queriesService = service.NewQueriesServiceMultiConnection(appDB, runners, defaultConnectionID, metricsCfg, emb, embeddingStore, cfg.Embedding.Model, readonlyUsers)
		queriesService.SetStatStatementsEnabled(cfg.Security.StatStatementsEnabled)
		reportsService = service.NewReportsServiceMultiConnection(appDB, runners, defaultConnectionID, llmClient, metricsCfg, emb, embeddingStore)
		reportsService.SetShareLinkDefaultHours(cfg.Security.ShareLinkDefaultHours)
		reportsService.SetShareLinksEnabled(cfg.Security.ShareLinksEnabled)
		reportsService.SetShareLinkExposeSQL(cfg.Security.ShareLinkExposeSQL)
		reportsService.SetPromptOptions(promptOpts)
		reportsService.SetLLMGovernance(llmAuditStore, budgetStore, cfg.LLM.AllowExternalData)
		reportsService.SetMaxLLMCallsPerReport(cfg.LLM.MaxCallsPerReport)
		suggester = pkgsuggestions.NewSuggesterWithEmbedding(appDB, emb, embeddingStore)
	} else {
		queriesService = service.NewQueriesServiceMultiConnection(appDB, runners, defaultConnectionID, metricsCfg, nil, nil, "", readonlyUsers)
		queriesService.SetStatStatementsEnabled(cfg.Security.StatStatementsEnabled)
		reportsService = service.NewReportsServiceMultiConnection(appDB, runners, defaultConnectionID, llmClient, metricsCfg, nil, nil)
		reportsService.SetShareLinkDefaultHours(cfg.Security.ShareLinkDefaultHours)
		reportsService.SetShareLinksEnabled(cfg.Security.ShareLinksEnabled)
		reportsService.SetShareLinkExposeSQL(cfg.Security.ShareLinkExposeSQL)
		reportsService.SetPromptOptions(promptOpts)
		reportsService.SetLLMGovernance(llmAuditStore, budgetStore, cfg.LLM.AllowExternalData)
		reportsService.SetMaxLLMCallsPerReport(cfg.LLM.MaxCallsPerReport)
		suggester = pkgsuggestions.NewSuggester(appDB)
	}
	schemaService := service.NewSchemaServiceMultiConnection(loaders, defaultConnectionID)
	askService := service.NewAskServiceMultiConnection(appDB, loaders, llmClient, validator, reportsService, defaultConnectionID)
	askService.SetLLMGovernance(llmAuditStore, budgetStore, cfg.LLM.AllowExternalData)
	suggestionsService := &service.SuggestionsServiceWrapper{Suggester: suggester, AskSvc: askService}
	connectionsService := service.NewConnectionsService(connectionItems)
	dashboardsService := service.NewDashboardsService(appDB, reportsService, queriesService)
	schedulesService := service.NewSchedulesService(appDB, queriesService, reportsService)
	schedulesService.SetRawPool(pools.App)
	schedulesService.ConfigureWebhook(cfg.Security.WebhookSigningSecret, cfg.Security.WebhookAllowedHosts)

	connAuthz := auth.NewConnectionAuthorizer(pools.App)
	queriesService.SetAuthorizer(connAuthz)
	reportsService.SetAuthorizer(connAuthz)
	schemaService.SetAuthorizer(connAuthz)
	askService.SetAuthorizer(connAuthz)
	schedulesService.SetAuthorizer(connAuthz)
	connectionsService.SetAuthorizer(connAuthz)

	reportsService.SetAuditStore(secAuditStore)
	encKey := []byte(strings.TrimSpace(cfg.Security.DataEncryptionKey))
	if len(encKey) == 0 {
		encKey = []byte(strings.TrimSpace(cfg.Security.SessionSecret))
	}
	queriesService.SetDataEncryptionKey(encKey)
	reportsService.SetDataEncryptionKey(encKey)

	return &Client{
		pools:              pools,
		queriesService:     queriesService,
		reportsService:     reportsService,
		dashboardsService:  dashboardsService,
		schedulesService:   schedulesService,
		schemaService:      schemaService,
		connectionsService: connectionsService,
		suggestionsService: suggestionsService,
		budgetStore:        budgetStore,
		auditStore:         secAuditStore,
	}, nil
}

// BudgetStore returns the LLM budget enforcer for background reservation cleanup.
func (c *Client) BudgetStore() *llm.BudgetStore {
	if c == nil {
		return nil
	}
	return c.budgetStore
}

// AuditStore returns the security audit store wired during NewClient.
func (c *Client) AuditStore() *audit.Store {
	if c == nil {
		return nil
	}
	return c.auditStore
}

// DashboardsService returns the dashboards service for use with Goa endpoints.
func (c *Client) DashboardsService() dashboards.Service {
	return c.dashboardsService
}

// SchedulesService returns the schedules service for use with Goa endpoints.
func (c *Client) SchedulesService() schedules.Service {
	return c.schedulesService
}

// SchedulesRunner returns the concrete schedules service for background
// execution (service.StartScheduleRunner, service.StartWebhookRetryWorker
// need methods, like RunDue and webhook delivery helpers, not on the
// schedules.Service Goa interface). Callers must treat the returned value as
// read-only/frozen: webhook signing secret, allowed hosts, and the
// authorizer are wired once in NewClient (see ConfigureWebhook, SetAuthorizer
// above) and must not be reconfigured here.
func (c *Client) SchedulesRunner() *service.SchedulesService {
	return c.schedulesService
}

func newLLMClient(cfg LLMConfig) llm.Client {
	switch cfg.Provider {
	case "ollama":
		return llm.NewOllamaClient(cfg.BaseURL, cfg.Model)
	case "gemini":
		return llm.NewGeminiClient(cfg.APIKey, cfg.Model)
	case "claude":
		return llm.NewClaudeClient(cfg.APIKey, cfg.Model)
	case "openai":
		return llm.NewOpenAIClient(cfg.APIKey, cfg.Model)
	case "groq":
		return llm.NewGroqClient(cfg.APIKey, cfg.Model)
	default:
		return llm.NewOllamaClient(cfg.BaseURL, cfg.Model)
	}
}

// Close releases database connection pools. Call once when the client is no longer needed.
func (c *Client) Close() {
	if c.pools != nil {
		c.pools.Close()
	}
}

// Ready returns nil if the database pools are reachable (for readiness probes).
func (c *Client) Ready(ctx context.Context) error {
	if c.pools == nil {
		return errors.New("client not initialized")
	}
	return c.pools.Health(ctx)
}

// HealthReport returns per-pool readiness for diagnostics and readiness JSON.
func (c *Client) HealthReport(ctx context.Context) []db.HealthStatus {
	if c == nil || c.pools == nil {
		return []db.HealthStatus{{Name: "client", Role: "client", Ready: false, Error: "not initialized"}}
	}
	return c.pools.HealthReport(ctx)
}

// NamedPools returns all configured pools with stable names for metrics.
func (c *Client) NamedPools() []db.NamedPool {
	if c == nil || c.pools == nil {
		return nil
	}
	return c.pools.NamedPools()
}

// QueriesService returns the queries service for use with Goa endpoints or direct calls.
func (c *Client) QueriesService() queries.Service {
	return c.queriesService
}

// ReportsService returns the reports service for use with Goa endpoints or direct calls.
func (c *Client) ReportsService() reports.Service {
	return c.reportsService
}

// SchemaService returns the schema service for use with Goa endpoints or direct calls.
func (c *Client) SchemaService() schema.Service {
	return c.schemaService
}

// SuggestionsService returns the suggestions service for use with Goa endpoints or direct calls.
func (c *Client) SuggestionsService() suggestions.Service {
	return c.suggestionsService
}

// ConnectionsService returns the connections service for use with Goa endpoints.
func (c *Client) ConnectionsService() connections.Service {
	return c.connectionsService
}

// AppPool returns the application database pool for use by the server (e.g. audit logging).
// Do not close the returned pool; Close the Client instead.
func (c *Client) AppPool() *pgxpool.Pool {
	if c == nil || c.pools == nil {
		return nil
	}
	return c.pools.App
}

func toAppConnections(in []DataConnectionConfig) []appconfig.DataConnectionConfig {
	out := make([]appconfig.DataConnectionConfig, 0, len(in))
	for _, c := range in {
		out = append(out, appconfig.DataConnectionConfig{
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
