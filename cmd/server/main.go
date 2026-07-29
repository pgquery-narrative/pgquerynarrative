// Package main provides the entry point for the PgQueryNarrative server.
// It initializes the HTTP server, sets up database connections, and starts
// serving API and web UI requests.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/pgquerynarrative/pgquerynarrative/api/gen/http/queries/server"
	reportsServer "github.com/pgquerynarrative/pgquerynarrative/api/gen/http/reports/server"
	schemaServer "github.com/pgquerynarrative/pgquerynarrative/api/gen/http/schema/server"
	suggestionsServer "github.com/pgquerynarrative/pgquerynarrative/api/gen/http/suggestions/server"
	"github.com/pgquerynarrative/pgquerynarrative/api/gen/queries"
	"github.com/pgquerynarrative/pgquerynarrative/api/gen/reports"
	schema "github.com/pgquerynarrative/pgquerynarrative/api/gen/schema"
	suggestions "github.com/pgquerynarrative/pgquerynarrative/api/gen/suggestions"
	"github.com/pgquerynarrative/pgquerynarrative/app/audit"
	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/app/config"
	"github.com/pgquerynarrative/pgquerynarrative/app/db"
	"github.com/pgquerynarrative/pgquerynarrative/app/httpmw"
	"github.com/pgquerynarrative/pgquerynarrative/app/llm"
	"github.com/pgquerynarrative/pgquerynarrative/app/observability"
	"github.com/pgquerynarrative/pgquerynarrative/app/ratelimit"
	"github.com/pgquerynarrative/pgquerynarrative/app/service"
	"github.com/pgquerynarrative/pgquerynarrative/gen/connections"
	"github.com/pgquerynarrative/pgquerynarrative/gen/dashboards"
	connectionsServer "github.com/pgquerynarrative/pgquerynarrative/gen/http/connections/server"
	dashboardsServer "github.com/pgquerynarrative/pgquerynarrative/gen/http/dashboards/server"
	investigationsServer "github.com/pgquerynarrative/pgquerynarrative/gen/http/investigations/server"
	schedulesServer "github.com/pgquerynarrative/pgquerynarrative/gen/http/schedules/server"
	workspaceServer "github.com/pgquerynarrative/pgquerynarrative/gen/http/workspace/server"
	"github.com/pgquerynarrative/pgquerynarrative/gen/investigations"
	"github.com/pgquerynarrative/pgquerynarrative/gen/schedules"
	"github.com/pgquerynarrative/pgquerynarrative/gen/workspace"
	"github.com/pgquerynarrative/pgquerynarrative/internal/logger"
	"github.com/pgquerynarrative/pgquerynarrative/pkg/narrative"
	"github.com/pgquerynarrative/pgquerynarrative/web"
	goahttp "goa.design/goa/v3/http"
)

const gracefulTimeout = 10 * time.Second

// Version is set at build time via -ldflags "-X main.Version=...". Default "dev".
var Version = "dev"

// contextKey type for request-scoped values.
type contextKey string

const requestIDContextKey contextKey = "request_id"

// main is the application entry point. It loads config, creates the narrative
// client (which owns DB pools, runner, LLM, and services), wires Goa endpoints
// and web UI to that client, and runs the HTTP server with graceful shutdown.
func main() {
	cfg := config.Load()
	if err := cfg.Validate(); err != nil {
		log.Fatalf("invalid configuration: %v", err)
	}
	if cfg.Security.AllowInsecureNoAuth {
		log.Printf("WARNING: SECURITY_ALLOW_INSECURE_NO_AUTH=true — authentication is disabled and the API uses an open-admin principal. Local/dev only; forbidden when APP_ENV=production.")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	client, err := narrative.NewClient(ctx, narrative.FromAppConfig(cfg))
	if err != nil {
		log.Fatalf("failed to create narrative client: %v", err)
	}
	defer client.Close()

	if config.StrictMode() {
		report := db.AuditSecurityBoundaryWithSecrets(ctx, client.AppPool(), cfg.Database, cfg.Security.DataEncryptionKey)
		if !report.OK {
			log.Fatalf("database security boundary check failed in production: %s", strings.Join(report.Issues, "; "))
		}
	}

	appLogger := logger.NewFromConfig(cfg.Logging.Level, cfg.Logging.Pretty)
	logger.SetDefault(appLogger)

	queriesEndpoints := queries.NewEndpoints(client.QueriesService())
	connectionsEndpoints := connections.NewEndpoints(client.ConnectionsService())
	reportsEndpoints := reports.NewEndpoints(client.ReportsService())
	dashboardsEndpoints := dashboards.NewEndpoints(client.DashboardsService())
	schedulesEndpoints := schedules.NewEndpoints(client.SchedulesService())
	schemaEndpoints := schema.NewEndpoints(client.SchemaService())
	suggestionsEndpoints := suggestions.NewEndpoints(client.SuggestionsService())
	investigationsEndpoints := investigations.NewEndpoints(client.InvestigationsService())
	workspaceEndpoints := workspace.NewEndpoints(client.WorkspaceService())

	if cfg.Security.ScheduleRunnerEnabled {
		// Webhook secret + allowlist are configured once in narrative.NewClient via ConfigureWebhook.
		service.StartScheduleRunner(ctx, client.AppPool(), client.SchedulesRunner(), cfg.Security.ScheduleRunnerInterval)
		service.StartWebhookRetryWorker(ctx, client.AppPool(), client.SchedulesRunner(), cfg.Security.ScheduleRunnerInterval)
		appLogger.Info("schedule runner started", "interval", cfg.Security.ScheduleRunnerInterval.String())
	}

	// Configure HTTP server
	httpServer, auditStore := setupHTTPServer(ctx, cfg, client, queriesEndpoints, connectionsEndpoints, reportsEndpoints, dashboardsEndpoints, schedulesEndpoints, schemaEndpoints, suggestionsEndpoints, investigationsEndpoints, workspaceEndpoints, appLogger)
	if auditStore != nil {
		defer auditStore.Close()
	}

	// Start server in a goroutine
	go func() {
		appLogger.Info("starting http server", "component", "api_server", "host_port", httpServer.Addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			appLogger.Err("server error", "error", err.Error())
			os.Exit(1)
		}
	}()

	// Wait for shutdown signal
	<-ctx.Done()
	appLogger.Info("shutting down server")

	shutdownTimeout := cfg.Server.ShutdownTimeout
	if shutdownTimeout <= 0 {
		shutdownTimeout = gracefulTimeout
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		appLogger.Err("shutdown error", "error", err.Error())
	} else {
		appLogger.Info("server stopped gracefully")
	}
}

// setupHTTPServer configures and returns an HTTP server with:
// - Health: GET /health (liveness), GET /ready (readiness with DB)
// - API routes (via Goa) at /api/v1/*
// - Web export and React SPA
func setupHTTPServer(
	ctx context.Context,
	cfg config.Config,
	client *narrative.Client,
	queriesEndpoints *queries.Endpoints,
	connectionsEndpoints *connections.Endpoints,
	reportsEndpoints *reports.Endpoints,
	dashboardsEndpoints *dashboards.Endpoints,
	schedulesEndpoints *schedules.Endpoints,
	schemaEndpoints *schema.Endpoints,
	suggestionsEndpoints *suggestions.Endpoints,
	investigationsEndpoints *investigations.Endpoints,
	workspaceEndpoints *workspace.Endpoints,
	appLogger *logger.Logger,
) (*http.Server, *audit.Store) {
	var auditStore *audit.Store
	if store := client.AuditStore(); store != nil {
		auditStore = store
	} else if pool := client.AppPool(); pool != nil {
		auditStore = audit.NewStore(pool, audit.ParseMode(cfg.Security.AuditMode))
	}
	// Gate high-risk business operations (query execution, report generation) on the audit
	// write itself: in audit.ModeRequired, a failed audit write blocks the operation (fail
	// closed) instead of letting it run unaudited. Must be applied before server.New copies
	// the endpoint functions below.
	queriesEndpoints.Use(auditGuardQueries(auditStore))
	reportsEndpoints.Use(auditGuardReports(auditStore))

	mux := goahttp.NewMuxer()
	dec := goahttp.RequestDecoder
	enc := goahttp.ResponseEncoder
	errHandler := func(ctx context.Context, w http.ResponseWriter, err error) {
		_ = goahttp.ErrorEncoder(enc, nil)(ctx, w, err)
	}

	queriesHTTP := server.New(queriesEndpoints, mux, dec, enc, errHandler, nil)
	server.Mount(mux, queriesHTTP)
	connectionsHTTP := connectionsServer.New(connectionsEndpoints, mux, dec, enc, errHandler, nil)
	connectionsServer.Mount(mux, connectionsHTTP)
	reportsHTTP := reportsServer.New(reportsEndpoints, mux, dec, enc, errHandler, nil)
	reportsServer.Mount(mux, reportsHTTP)
	dashboardsHTTP := dashboardsServer.New(dashboardsEndpoints, mux, dec, enc, errHandler, nil)
	dashboardsServer.Mount(mux, dashboardsHTTP)
	schedulesHTTP := schedulesServer.New(schedulesEndpoints, mux, dec, enc, errHandler, nil)
	schedulesServer.Mount(mux, schedulesHTTP)
	schemaHTTP := schemaServer.New(schemaEndpoints, mux, dec, enc, errHandler, nil)
	schemaServer.Mount(mux, schemaHTTP)
	suggestionsHTTP := suggestionsServer.New(suggestionsEndpoints, mux, dec, enc, errHandler, nil)
	suggestionsServer.Mount(mux, suggestionsHTTP)
	investigationsHTTP := investigationsServer.New(investigationsEndpoints, mux, dec, enc, errHandler, nil)
	investigationsServer.Mount(mux, investigationsHTTP)
	workspaceHTTP := workspaceServer.New(workspaceEndpoints, mux, dec, enc, errHandler, nil)
	workspaceServer.Mount(mux, workspaceHTTP)

	webHandlers := web.NewHandlers(queriesEndpoints, reportsEndpoints)

	oidc := auth.NewOIDCValidator(auth.OIDCConfig{
		Issuer:     cfg.Security.OIDCIssuer,
		Audience:   cfg.Security.OIDCAudience,
		JWKSURL:    cfg.Security.OIDCJWKSURL,
		StrictMode: config.StrictMode(),
	})
	sessions := auth.NewSessionManager(cfg.Security.SessionSecret, cfg.Security.SessionTTL, config.StrictMode())
	membership := auth.NewMembershipStore(client.AppPool(), cfg.Security.OIDCAutoJoinDefaultOrg)
	if sessions != nil {
		sessions.AttachSessionStore(auth.NewSessionStore(client.AppPool()))
	}

	combinedMux := http.NewServeMux()
	combinedMux.HandleFunc("/health", healthHandler)
	combinedMux.HandleFunc("/ready", readyHandler(client))
	combinedMux.HandleFunc("/ready/connections", connectionHealthHandler(client))
	combinedMux.HandleFunc("/version", versionHandler())
	combinedMux.HandleFunc("/metrics", metricsHandler(client))
	combinedMux.HandleFunc("/api/v1/settings", settingsHandler(cfg))
	var browserOIDC *auth.BrowserOIDC
	if browser := auth.NewBrowserOIDC(auth.BrowserOIDCConfig{
		Issuer:       cfg.Security.OIDCIssuer,
		ClientID:     cfg.Security.OIDCClientID,
		ClientSecret: cfg.Security.OIDCClientSecret,
		RedirectURL:  cfg.Security.OIDCRedirectURL,
		Audience:     cfg.Security.OIDCAudience,
	}, oidc, sessions); browser != nil && browser.Enabled() {
		browserOIDC = browser
		browser.SetMembershipStore(membership)
		browser.SetPKCEStore(auth.NewPKCEStore(client.AppPool()))
		combinedMux.HandleFunc("/auth/login", browser.LoginHandler)
		combinedMux.HandleFunc("/auth/callback", browser.CallbackHandler)
		combinedMux.HandleFunc("/auth/logout", browser.LogoutHandler)
		combinedMux.HandleFunc("/auth/refresh", browser.RefreshHandler)
	}
	if sessions != nil && sessions.Enabled() {
		combinedMux.HandleFunc("/auth/session", sessions.StatusHandler)
		if browserOIDC == nil {
			combinedMux.HandleFunc("/auth/refresh", sessions.RefreshHandler)
		}
	}
	combinedMux.HandleFunc("/api/v1/diagnostics/db-privileges", dbPrivilegesHandler(cfg, client))
	combinedMux.HandleFunc("/api/v1/diagnostics/webhook-policy", webhookPolicyHandler(client))
	combinedMux.Handle("/api/", mux)
	combinedMux.HandleFunc("/web/reports/export", webHandlers.ExportReport)
	combinedMux.HandleFunc("/web/reports/export/pdf", webHandlers.ExportReportPDF)
	combinedMux.HandleFunc("/web/reports/export/shared/pdf", webHandlers.ExportSharedReportPDF)
	combinedMux.Handle("/", spaHandler("frontend/dist"))

	authenticator := auth.NewAuthenticator(
		cfg.Security.AuthEnabled,
		cfg.Security.APIKey,
		cfg.Security.APIKeyHash,
		cfg.Security.APIKeysJSON,
		oidc,
	)
	authenticator.SetMembershipStore(membership)
	authenticator.SetKeyUsageStore(auth.NewKeyUsageStore(client.AppPool()))
	managedKeys := auth.NewManagedKeyStore(client.AppPool())
	authenticator.SetManagedKeyStore(managedKeys)
	connAuthz := auth.NewConnectionAuthorizer(client.AppPool())
	connAuthz.SetAllowlistRequired(cfg.Security.ConnectionAllowlistRequired)
	orgSecrets := auth.NewOrgConnectionSecretStore(client.AppPool(), cfg.Security.DataEncryptionKey)
	mountAdminAPI(combinedMux, adminDeps{
		keys:       managedKeys,
		membership: membership,
		connAuthz:  connAuthz,
		auditStore: auditStore,
		sessions:   sessions,
		encKey:     cfg.Security.DataEncryptionKey,
		orgSecrets: orgSecrets,
		pools:      client.PoolManager(),
	})
	mountMeAPI(combinedMux, meDeps{
		membership: membership,
		sessions:   sessions,
	})
	failureMode := ratelimit.ParseFailureMode(cfg.Security.RateLimitFailureMode)
	rl := ratelimit.NewLimiterFromConfig(client.AppPool(), cfg.Security.RateLimitRPM, cfg.Security.RateLimitBurst, cfg.Security.RateLimitDistributed, failureMode)
	if pgl, ok := rl.(*ratelimit.PostgresLimiter); ok {
		maxAge := cfg.Security.RateLimitBucketMaxAge
		if maxAge <= 0 {
			maxAge = 24 * time.Hour
		}
		pgl.StartCleanupLoop(ctx, 10*time.Minute, maxAge)
	}
	// Bound how long redacted EXPLAIN snapshots (sql_text, plan JSON) are retained at rest.
	// ExplainSnapshotRetentionDays <= 0 disables the loop and keeps snapshots forever.
	service.StartExplainSnapshotCleanupLoop(ctx, client.AppPool(), 6*time.Hour, cfg.Security.ExplainSnapshotRetentionDays)
	regressionPoller := service.NewRegressionPoller(client.AppPool(), client.QueriesRunner(), service.RegressionPollerConfig{
		Enabled:              cfg.Security.RegressionPollerEnabled && cfg.Security.StatStatementsEnabled,
		Interval:             cfg.Security.RegressionPollerInterval,
		MeanTimeThresholdPct: cfg.Security.RegressionMeanThresholdPct,
		CriticalThresholdPct: cfg.Security.RegressionCriticalThresholdPct,
		HighThresholdPct:     cfg.Security.RegressionHighThresholdPct,
		RetentionDays:        cfg.Security.RegressionSnapshotRetentionDays,
	})
	service.StartRegressionPollerLoop(ctx, regressionPoller)
	// Reclaim abandoned LLM budget reservations left by crashed workers.
	llm.StartReservationCleanupLoop(ctx, client.BudgetStore(), 5*time.Minute)
	trusted := newTrustedProxyMatcher(cfg.Security.TrustedProxies)

	handler := observabilityMiddleware(requestIDMiddleware(requestLoggingMiddleware(combinedMux, appLogger, auditStore, trusted)))
	handler = maxBodyMiddleware(handler, cfg.Security.MaxRequestBodyBytes)
	handler = httpmw.AuthMiddleware(handler, authenticator, sessions, auditStore, trusted)
	handler = httpmw.RateLimitMiddleware(handler, rl, auditStore, trusted, authenticator, sessions, failureMode, config.StrictMode())
	handler = securityHeadersMiddleware(handler)
	if len(cfg.Server.CORSOrigins) > 0 {
		handler = corsMiddleware(handler, cfg.Server.CORSOrigins)
	}

	return &http.Server{
		Addr:         cfg.Server.Host + ":" + strconv.Itoa(cfg.Server.Port),
		Handler:      handler,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
		IdleTimeout:  120 * time.Second,
	}, auditStore
}

func healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("OK"))
}

func readyHandler(client *narrative.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("format")), "json") {
			statuses := client.HealthReport(r.Context())
			ready := true
			for _, st := range statuses {
				if !st.Ready {
					ready = false
					break
				}
			}
			w.Header().Set("Content-Type", "application/json")
			if !ready {
				w.WriteHeader(http.StatusServiceUnavailable)
			} else {
				w.WriteHeader(http.StatusOK)
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"ready": ready, "pools": statuses})
			return
		}
		if err := client.Ready(r.Context()); err != nil {
			appLogger := logger.DefaultLogger()
			appLogger.Err("ready check failed", "error", err.Error())
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	}
}

func connectionHealthHandler(client *narrative.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		statuses := client.HealthReport(r.Context())
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"pools": statuses})
	}
}

func versionHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"version": Version})
	}
}

// toPoolMetrics adapts db.NamedPool (used by narrative.Client) to observability.PoolMetric.
// observability cannot import app/db directly without creating an import cycle
// (app/db -> app/config -> app/audit -> app/observability), so the conversion lives here.
func toPoolMetrics(named []db.NamedPool) []observability.PoolMetric {
	out := make([]observability.PoolMetric, len(named))
	for i, n := range named {
		out[i] = observability.PoolMetric{Name: n.Name, Role: n.Role, Pool: n.Pool}
	}
	return out
}

func metricsHandler(client *narrative.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
		if format == "" {
			accept := r.Header.Get("Accept")
			if strings.Contains(accept, "text/plain") && strings.Contains(accept, "openmetrics") {
				format = "prometheus"
			}
		}
		if format == "prometheus" || format == "openmetrics" {
			w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(observability.PrometheusAllPoolMetrics(Version, toPoolMetrics(client.NamedPools()))))
			return
		}
		out := map[string]interface{}{"version": Version}
		if pool := client.AppPool(); pool != nil {
			stat := pool.Stat()
			out["pool"] = map[string]int32{
				"acquired": stat.AcquiredConns(),
				"idle":     stat.IdleConns(),
				"total":    stat.TotalConns(),
				"max":      stat.MaxConns(),
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(out)
	}
}

// settingsHandler returns read-only analytics, LLM, and embedding configuration (env-driven). Used by Settings UI.
func settingsHandler(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		embEnabled := cfg.Embedding.BaseURL != "" && cfg.Embedding.Model != ""
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"security": map[string]interface{}{
				"auth_enabled":   cfg.Security.AuthEnabled,
				"rate_limit_rpm": cfg.Security.RateLimitRPM,
			},
			"analytics": map[string]interface{}{
				"anomaly_sigma":               cfg.Metrics.AnomalySigma,
				"anomaly_method":              cfg.Metrics.AnomalyMethod,
				"trend_periods":               cfg.Metrics.TrendPeriods,
				"moving_avg_window":           cfg.Metrics.MovingAvgWindow,
				"trend_threshold_percent":     cfg.Metrics.TrendThresholdPercent,
				"confidence_level":            cfg.Metrics.ConfidenceLevel,
				"min_rows_for_correlation":    cfg.Metrics.MinRowsForCorrelation,
				"smoothing_alpha":             cfg.Metrics.SmoothingAlpha,
				"smoothing_beta":              cfg.Metrics.SmoothingBeta,
				"max_seasonal_lag":            cfg.Metrics.MaxSeasonalLag,
				"min_periods_for_seasonality": cfg.Metrics.MinPeriodsForSeasonality,
				"max_timeseries_periods":      cfg.Metrics.MaxTimeSeriesPeriods,
			},
			"llm": map[string]interface{}{
				"provider":           cfg.LLM.Provider,
				"model":              cfg.LLM.Model,
				"base_url":           cfg.LLM.BaseURL,
				"api_key_configured": cfg.LLM.APIKey != "",
			},
			"embedding": map[string]interface{}{
				"enabled":  embEnabled,
				"base_url": cfg.Embedding.BaseURL,
				"model":    cfg.Embedding.Model,
			},
		})
	}
}

func dbPrivilegesHandler(cfg config.Config, client *narrative.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !auth.IsPlatformAdminRole(auth.RoleFromContext(r.Context())) {
			auth.WriteForbidden(w)
			return
		}
		report := db.AuditSecurityBoundary(r.Context(), client.AppPool(), cfg.Database)
		w.Header().Set("Content-Type", "application/json")
		if !report.OK {
			w.WriteHeader(http.StatusServiceUnavailable)
		} else {
			w.WriteHeader(http.StatusOK)
		}
		_ = json.NewEncoder(w).Encode(report)
	}
}

// webhookPolicyHandler exposes the active webhook hostname allowlist (no secrets) to admins.
func webhookPolicyHandler(client *narrative.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !auth.IsPlatformAdminRole(auth.RoleFromContext(r.Context())) {
			auth.WriteForbidden(w)
			return
		}
		hosts := client.SchedulesRunner().WebhookAllowedHosts()
		if hosts == nil {
			hosts = []string{}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"allowed_hosts":    hosts,
			"allowlist_active": len(hosts) > 0,
		})
	}
}

// requestIDMiddleware generates a request ID, sets it in context and X-Request-ID header.
func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			b := make([]byte, 16)
			if _, err := rand.Read(b); err == nil {
				id = hex.EncodeToString(b)
			} else {
				id = strconv.FormatInt(time.Now().UnixNano(), 36)
			}
		}
		ctx := context.WithValue(r.Context(), requestIDContextKey, id)
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func observabilityMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		observability.IncRequest()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		observability.ObserveRequestDuration(time.Since(start))
		if rec.status >= 500 {
			observability.IncRequestError()
		}
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'self'")
		if isHTTPSRequest(r) {
			// App typically terminates TLS at ingress; honor forwarded proto.
			w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

func isHTTPSRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	proto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto"))
	return strings.EqualFold(proto, "https")
}

func maxBodyMiddleware(next http.Handler, maxBytes int64) http.Handler {
	if maxBytes <= 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
		}
		next.ServeHTTP(w, r)
	})
}

func corsMiddleware(next http.Handler, origins []string) http.Handler {
	originSet := make(map[string]bool, len(origins))
	for _, o := range origins {
		originSet[strings.TrimSpace(o)] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && originSet[origin] {
			w.Header().Set("Access-Control-Allow-Origin", origin)
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Access-Control-Max-Age", "86400")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requestLoggingMiddleware logs each HTTP request and records API_REQUEST in the audit log when auditStore is set.
func requestLoggingMiddleware(next http.Handler, appLogger *logger.Logger, auditStore *audit.Store, trusted *trustedProxyMatcher) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		clientIP := clientIPFromRequest(r, trusted)
		path := r.URL.Path
		if path == "" {
			path = "/"
		}
		method := r.Method

		wrapped := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK, logger: appLogger, method: method, path: path}
		next.ServeHTTP(wrapped, r)

		duration := time.Since(start).Round(time.Millisecond)
		kvs := []interface{}{"component", "http", "client_ip", clientIP, "method", method, "path", path, "http.status_code", wrapped.statusCode, "duration_ms", duration.Milliseconds()}
		if reqID, ok := r.Context().Value(requestIDContextKey).(string); ok && reqID != "" {
			kvs = append(kvs, "request_id", reqID)
		}
		switch {
		case wrapped.statusCode >= 400:
			appLogger.Err("http request", kvs...)
		case path == "/health" || path == "/ready" || path == "/version":
			appLogger.Debug("http request", kvs...)
		default:
			appLogger.Info("http request", kvs...)
		}
		wrapped.logErrorIfAny()

		if auditStore != nil && (strings.HasPrefix(path, "/api/") || path == "/web/reports/export" || path == "/web/reports/export/pdf") {
			identity, _ := r.Context().Value(auth.IdentityContextKey).(string)
			_ = auditStore.Record(r.Context(), audit.Entry{
				EventType: apiRequestEventType(method, path),
				Details:   map[string]interface{}{"method": method, "path": path, "status_code": wrapped.statusCode},
				UserID:    identity,
				IP:        clientIP,
				UserAgent: r.UserAgent(),
			})
		}
	})
}

// apiRequestEventType classifies a request into a specific app.audit_logs event type for
// known high-value operations (query execution, report generation/export, saved-query
// mutations); everything else is recorded as the generic API_REQUEST. This is a completion
// record (includes the final status code) and is independent of the pre-execution,
// fail-closed-capable RUN_QUERY/GENERATE_REPORT entries recorded by auditGuardQueries/
// auditGuardReports.
func apiRequestEventType(method, path string) string {
	switch {
	case path == "/api/v1/queries/run" && method == http.MethodPost:
		return audit.EventRunQuery
	case (path == "/api/v1/reports/generate" || path == "/api/v1/reports/rewrite") && method == http.MethodPost:
		return audit.EventGenerateReport
	case path == "/web/reports/export" || path == "/web/reports/export/pdf":
		return audit.EventExportReport
	case path == "/api/v1/queries/saved" && method == http.MethodPost:
		return audit.EventSaveQuery
	case strings.HasPrefix(path, "/api/v1/queries/saved/") && method == http.MethodDelete:
		return audit.EventDeleteQuery
	default:
		return audit.EventAPIRequest
	}
}

type responseWriter struct {
	http.ResponseWriter
	statusCode int
	body       bytes.Buffer
	capture    bool
	logger     *logger.Logger
	method     string
	path       string
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	if code >= 400 {
		rw.capture = true
	}
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(p []byte) (n int, err error) {
	if rw.capture && rw.body.Len() < 2048 {
		rw.body.Write(p)
	}
	return rw.ResponseWriter.Write(p)
}

func (rw *responseWriter) logErrorIfAny() {
	if rw.statusCode < 400 || rw.logger == nil {
		return
	}
	body := strings.TrimSpace(rw.body.String())
	const max = 512
	if len(body) > max {
		body = body[:max] + "..."
	}
	body = strings.ReplaceAll(body, "\n", " ")
	if body == "" {
		rw.logger.Err("error response", "component", "http", "status", rw.statusCode, "method", rw.method, "path", rw.path)
		return
	}
	rw.logger.Err("error response", "component", "http", "status", rw.statusCode, "method", rw.method, "path", rw.path, "body", body)
}

// spaHandler serves a React SPA: static files from dir, fallback to index.html for client-side routes.
// Sets Cache-Control: index.html no-cache (always revalidate); hashed assets long-lived.
func spaHandler(dir string) http.Handler {
	fs := http.Dir(dir)
	fileServer := http.FileServer(fs)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/" {
			path = "/index.html"
		}
		if f, err := fs.Open(path); err == nil {
			f.Close() // #nosec G104 -- best-effort close of a just-opened static asset handle; nothing actionable on error.
			if path == "/index.html" {
				w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
			} else if strings.Contains(path, "-") && (strings.HasSuffix(path, ".js") || strings.HasSuffix(path, ".css")) {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			fileServer.ServeHTTP(w, r)
			return
		}
		r.URL.Path = "/"
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		fileServer.ServeHTTP(w, r)
	})
}
