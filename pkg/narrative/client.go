package narrative

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgquerynarrative/pgquerynarrative/api/gen/queries"
	"github.com/pgquerynarrative/pgquerynarrative/api/gen/reports"
	schema "github.com/pgquerynarrative/pgquerynarrative/api/gen/schema"
	suggestions "github.com/pgquerynarrative/pgquerynarrative/api/gen/suggestions"
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
}

// NewClient builds a narrative client from the given config. It creates
// database pools, query runner, LLM client, and all services. The returned
// client must be closed to release resources.
func NewClient(ctx context.Context, cfg Config) (*Client, error) {
	dbCfg := appconfig.DatabaseConfig{
		Host:             cfg.Database.Host,
		Port:             cfg.Database.Port,
		Database:         cfg.Database.Database,
		User:             cfg.Database.User,
		Password:         cfg.Database.Password,
		MaxConnections:   cfg.Database.MaxConnections,
		ReadOnlyUser:     cfg.Database.ReadOnlyUser,
		ReadOnlyPassword: cfg.Database.ReadOnlyPassword,
		SSLMode:          cfg.Database.SSLMode,
		QueryTimeout:     cfg.Database.QueryTimeout,
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
		roPool := pools.ReadOnly(conn.ID)
		connSchemas := conn.AllowedSchemas
		if len(connSchemas) == 0 {
			connSchemas = allowedSchemas
		}
		connValidator := queryrunner.NewValidator(connSchemas, maxQueryLength)
		timeout := conn.QueryTimeout
		if timeout <= 0 {
			timeout = cfg.Database.QueryTimeout
		}
		runners[conn.ID] = queryrunner.NewRunner(roPool, connValidator, maxRows, timeout, queryrunner.WithExplainAnalyze(cfg.Security.ExplainAnalyzeEnabled))
		loaders[conn.ID] = catalog.NewLoader(roPool, connSchemas)
		readonlyUsers[conn.ID] = conn.ReadOnlyUser
		connectionItems = append(connectionItems, &connections.ConnectionInfo{ID: conn.ID, Name: conn.Name})
	}

	promptOpts := llm.PromptOptions{
		MaxSampleRows: cfg.LLM.MaxSampleRows,
		SendRowData:   cfg.LLM.SendRowData,
		RedactPII:     cfg.LLM.RedactPII,
	}

	var queriesService *service.QueriesService
	var reportsService *service.ReportsService
	var suggester *pkgsuggestions.Suggester
	embeddingStore := embedding.NewStore(pools.App)

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
	}
	if cfg.Embedding.BaseURL != "" && cfg.Embedding.Model != "" {
		emb := embedding.NewOllamaEmbedder(cfg.Embedding.BaseURL, cfg.Embedding.Model)
		queriesService = service.NewQueriesServiceMultiConnection(pools.App, runners, defaultConnectionID, metricsCfg, emb, embeddingStore, cfg.Embedding.Model, readonlyUsers)
		queriesService.SetStatStatementsEnabled(cfg.Security.StatStatementsEnabled)
		reportsService = service.NewReportsServiceMultiConnection(pools.App, runners, defaultConnectionID, llmClient, metricsCfg, emb, embeddingStore)
		reportsService.SetShareLinkDefaultHours(cfg.Security.ShareLinkDefaultHours)
		reportsService.SetPromptOptions(promptOpts)
		suggester = pkgsuggestions.NewSuggesterWithEmbedding(pools.App, emb, embeddingStore)
	} else {
		queriesService = service.NewQueriesServiceMultiConnection(pools.App, runners, defaultConnectionID, metricsCfg, nil, nil, "", readonlyUsers)
		queriesService.SetStatStatementsEnabled(cfg.Security.StatStatementsEnabled)
		reportsService = service.NewReportsServiceMultiConnection(pools.App, runners, defaultConnectionID, llmClient, metricsCfg, nil, nil)
		reportsService.SetShareLinkDefaultHours(cfg.Security.ShareLinkDefaultHours)
		reportsService.SetPromptOptions(promptOpts)
		suggester = pkgsuggestions.NewSuggester(pools.App)
	}
	schemaService := service.NewSchemaServiceMultiConnection(loaders, defaultConnectionID)
	askService := service.NewAskServiceMultiConnection(pools.App, loaders, llmClient, validator, reportsService, defaultConnectionID)
	suggestionsService := &service.SuggestionsServiceWrapper{Suggester: suggester, AskSvc: askService}
	connectionsService := service.NewConnectionsService(connectionItems)
	dashboardsService := service.NewDashboardsService(pools.App, reportsService, queriesService)
	schedulesService := service.NewSchedulesService(pools.App, queriesService, reportsService)

	return &Client{
		pools:              pools,
		queriesService:     queriesService,
		reportsService:     reportsService,
		dashboardsService:  dashboardsService,
		schedulesService:   schedulesService,
		schemaService:      schemaService,
		connectionsService: connectionsService,
		suggestionsService: suggestionsService,
	}, nil
}

// DashboardsService returns the dashboards service for use with Goa endpoints.
func (c *Client) DashboardsService() dashboards.Service {
	return c.dashboardsService
}

// SchedulesService returns the schedules service for use with Goa endpoints.
func (c *Client) SchedulesService() schedules.Service {
	return c.schedulesService
}

// SchedulesRunner returns the concrete schedules service for background execution.
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
			AllowedSchemas:   append([]string(nil), c.AllowedSchemas...),
		})
	}
	return out
}
