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
	"github.com/pgquerynarrative/pgquerynarrative/app/logger"
	"github.com/pgquerynarrative/pgquerynarrative/app/observability"
	"github.com/pgquerynarrative/pgquerynarrative/app/ratelimit"
	"github.com/pgquerynarrative/pgquerynarrative/app/service"
	"github.com/pgquerynarrative/pgquerynarrative/gen/connections"
	"github.com/pgquerynarrative/pgquerynarrative/gen/dashboards"
	connectionsServer "github.com/pgquerynarrative/pgquerynarrative/gen/http/connections/server"
	dashboardsServer "github.com/pgquerynarrative/pgquerynarrative/gen/http/dashboards/server"
	schedulesServer "github.com/pgquerynarrative/pgquerynarrative/gen/http/schedules/server"
	"github.com/pgquerynarrative/pgquerynarrative/gen/schedules"
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
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	client, err := narrative.NewClient(ctx, narrative.FromAppConfig(cfg))
	if err != nil {
		log.Fatalf("failed to create narrative client: %v", err)
	}
	defer client.Close()

	appLogger := logger.NewFromConfig(cfg.Logging.Level, cfg.Logging.Pretty)
	logger.SetDefault(appLogger)

	queriesEndpoints := queries.NewEndpoints(client.QueriesService())
	connectionsEndpoints := connections.NewEndpoints(client.ConnectionsService())
	reportsEndpoints := reports.NewEndpoints(client.ReportsService())
	dashboardsEndpoints := dashboards.NewEndpoints(client.DashboardsService())
	schedulesEndpoints := schedules.NewEndpoints(client.SchedulesService())
	schemaEndpoints := schema.NewEndpoints(client.SchemaService())
	suggestionsEndpoints := suggestions.NewEndpoints(client.SuggestionsService())

	if cfg.Security.ScheduleRunnerEnabled {
		service.StartScheduleRunner(ctx, client.AppPool(), client.SchedulesRunner(), cfg.Security.ScheduleRunnerInterval)
		appLogger.Info("schedule runner started", "interval", cfg.Security.ScheduleRunnerInterval.String())
	}

	// Configure HTTP server
	httpServer := setupHTTPServer(cfg, client, queriesEndpoints, connectionsEndpoints, reportsEndpoints, dashboardsEndpoints, schedulesEndpoints, schemaEndpoints, suggestionsEndpoints, appLogger)

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
	cfg config.Config,
	client *narrative.Client,
	queriesEndpoints *queries.Endpoints,
	connectionsEndpoints *connections.Endpoints,
	reportsEndpoints *reports.Endpoints,
	dashboardsEndpoints *dashboards.Endpoints,
	schedulesEndpoints *schedules.Endpoints,
	schemaEndpoints *schema.Endpoints,
	suggestionsEndpoints *suggestions.Endpoints,
	appLogger *logger.Logger,
) *http.Server {
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

	webHandlers := web.NewHandlers(queriesEndpoints, reportsEndpoints)

	combinedMux := http.NewServeMux()
	combinedMux.HandleFunc("/health", healthHandler)
	combinedMux.HandleFunc("/ready", readyHandler(client))
	combinedMux.HandleFunc("/version", versionHandler())
	combinedMux.HandleFunc("/metrics", metricsHandler(client))
	combinedMux.HandleFunc("/api/v1/settings", settingsHandler(cfg))
	combinedMux.Handle("/api/", mux)
	combinedMux.HandleFunc("/web/reports/export", webHandlers.ExportReport)
	combinedMux.HandleFunc("/web/reports/export/pdf", webHandlers.ExportReportPDF)
	combinedMux.HandleFunc("/web/reports/export/shared/pdf", webHandlers.ExportSharedReportPDF)
	combinedMux.Handle("/", spaHandler("frontend/dist"))

	var auditStore *audit.Store
	if pool := client.AppPool(); pool != nil {
		auditStore = audit.NewStore(pool)
	}
	oidc := auth.NewOIDCValidator(auth.OIDCConfig{
		Issuer:   cfg.Security.OIDCIssuer,
		Audience: cfg.Security.OIDCAudience,
		JWKSURL:  cfg.Security.OIDCJWKSURL,
	})
	authenticator := auth.NewAuthenticator(
		cfg.Security.AuthEnabled,
		cfg.Security.APIKey,
		cfg.Security.APIKeysJSON,
		oidc,
	)
	rl := ratelimit.NewLimiterFromConfig(client.AppPool(), cfg.Security.RateLimitRPM, cfg.Security.RateLimitBurst, cfg.Security.RateLimitDistributed)
	trusted := newTrustedProxyMatcher(cfg.Security.TrustedProxies)

	handler := requestIDMiddleware(requestLoggingMiddleware(combinedMux, appLogger, auditStore, trusted))
	handler = maxBodyMiddleware(handler, cfg.Security.MaxRequestBodyBytes)
	handler = authMiddleware(handler, authenticator, auditStore, trusted)
	handler = rateLimitMiddleware(handler, rl, auditStore, trusted)
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
	}
}

func healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("OK"))
}

func readyHandler(client *narrative.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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

func versionHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"version": Version})
	}
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
			_, _ = w.Write([]byte(observability.PrometheusPoolMetrics(Version, client.AppPool())))
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

// authMiddleware requires Bearer token for /api/* and /web/reports/export* when enabled.
// Sets auth identity and role in context for audit and RBAC. Health and ready are never protected.
func authMiddleware(next http.Handler, authenticator *auth.Authenticator, auditStore *audit.Store, trusted *trustedProxyMatcher) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "" {
			path = "/"
		}
		isPublicSharedAPI := strings.HasPrefix(path, "/api/v1/reports/shared/")
		isPublicSharedPDF := path == "/web/reports/export/shared/pdf"
		needAuth := (strings.HasPrefix(path, "/api/") && !isPublicSharedAPI) ||
			path == "/metrics" ||
			((path == "/web/reports/export" || path == "/web/reports/export/pdf") && !isPublicSharedPDF)
		if authenticator == nil || !authenticator.AuthRequired() || !needAuth {
			next.ServeHTTP(w, r)
			return
		}
		identity, role, ok := authenticator.ValidateRequest(r)
		if !ok {
			clientIP := clientIPFromRequest(r, trusted)
			if auditStore != nil {
				auditStore.Record(r.Context(), audit.Entry{
					EventType: audit.EventAuthFailure,
					Details:   map[string]interface{}{"path": path},
					IP:        clientIP,
					UserAgent: r.UserAgent(),
				})
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"name":"unauthorized","message":"missing or invalid Authorization","code":"UNAUTHORIZED"}`))
			return
		}
		if !auth.AllowsMethod(role, r.Method, path) {
			auth.WriteForbidden(w)
			return
		}
		ctx := context.WithValue(r.Context(), auth.IdentityContextKey, identity)
		ctx = context.WithValue(ctx, auth.RoleContextKey, role)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// rateLimitMiddleware limits requests per client IP when limiter is non-nil. Returns 429 when exceeded.
func rateLimitMiddleware(next http.Handler, limiter ratelimit.AllowFunc, auditStore *audit.Store, trusted *trustedProxyMatcher) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if limiter == nil {
			next.ServeHTTP(w, r)
			return
		}
		key := clientIPFromRequest(r, trusted)
		if !limiter.Allow(key) {
			if auditStore != nil {
				auditStore.Record(r.Context(), audit.Entry{
					EventType: audit.EventRateLimitExceeded,
					Details:   map[string]interface{}{"path": r.URL.Path, "client": key},
					IP:        key,
					UserAgent: r.UserAgent(),
				})
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"name":"rate_limit_exceeded","message":"too many requests","code":"RATE_LIMIT_EXCEEDED"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'self'")
		next.ServeHTTP(w, r)
	})
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
			auditStore.Record(r.Context(), audit.Entry{
				EventType: audit.EventAPIRequest,
				Details:   map[string]interface{}{"method": method, "path": path, "status_code": wrapped.statusCode},
				UserID:    identity,
				IP:        clientIP,
				UserAgent: r.UserAgent(),
			})
		}
	})
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
			f.Close()
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
