// Package service provides business logic for queries and reports.
// It acts as a bridge between the API layer and the data/query execution layer.
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/api/gen/queries"
	"github.com/pgquerynarrative/pgquerynarrative/app/apilog"
	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/app/catalog"
	"github.com/pgquerynarrative/pgquerynarrative/app/charts"
	"github.com/pgquerynarrative/pgquerynarrative/app/config"
	"github.com/pgquerynarrative/pgquerynarrative/app/db"
	"github.com/pgquerynarrative/pgquerynarrative/app/embedding"
	apperrors "github.com/pgquerynarrative/pgquerynarrative/app/errors"
	"github.com/pgquerynarrative/pgquerynarrative/app/format"
	"github.com/pgquerynarrative/pgquerynarrative/app/llm"
	"github.com/pgquerynarrative/pgquerynarrative/app/metrics"
	"github.com/pgquerynarrative/pgquerynarrative/app/observability"
	"github.com/pgquerynarrative/pgquerynarrative/app/queryrunner"
	"github.com/pgquerynarrative/pgquerynarrative/app/security"
)

// QueriesService handles query execution and saved query management.
type QueriesService struct {
	readOnlyPool   *pgxpool.Pool       // Pool for executing queries (read-only)
	appPool        db.DB               // Pool for saving queries (full access)
	runner         *queryrunner.Runner // Query execution engine
	metricsOpts    *metrics.Options    // Metrics and time-series options (windows, thresholds)
	embedder       embedding.Embedder  // Optional: for storing query embeddings on save
	embeddingStore *embedding.Store    // Optional: for RAG / similar-query retrieval
	embeddingModel string              // Model name to store with embedding (e.g. nomic-embed-text)
	connectionResolver
	statStatementsEnabled bool
	authz                 ConnectionAuthorizer
	dataEncKey            []byte
}

// SetStatStatementsEnabled toggles the pg_stat_statements API.
func (s *QueriesService) SetStatStatementsEnabled(enabled bool) {
	s.statStatementsEnabled = enabled
}

// SetDataEncryptionKey configures AES-GCM sealing for EXPLAIN snapshots at rest.
// Intended to be called only once from narrative.NewClient.
func (s *QueriesService) SetDataEncryptionKey(key []byte) {
	if s != nil && len(key) > 0 {
		s.dataEncKey = append([]byte(nil), key...)
	}
}

// SetAuthorizer wires connection-level authorization (C5). Nil is permissive.
// Intended to be called only once, from narrative.NewClient, before the
// service is handed to any HTTP handler or background worker.
func (s *QueriesService) SetAuthorizer(authz ConnectionAuthorizer) {
	if s != nil {
		s.authz = authz
	}
}

var strPtr = format.StrPtr

// NewQueriesService creates a new queries service with the specified dependencies.
// metricsCfg supplies trend threshold, anomaly sigma, trend periods, and moving-average window; nil uses defaults.
func NewQueriesService(readOnlyPool *pgxpool.Pool, appPool db.DB, runner *queryrunner.Runner, metricsCfg config.MetricsConfig) *QueriesService {
	defaultRunners := map[string]*queryrunner.Runner{"default": runner}
	defaultLoaders := map[string]*catalog.Loader{}
	opts := metricsOptionsFromConfig(metricsCfg)
	return &QueriesService{
		readOnlyPool:       readOnlyPool,
		appPool:            appPool,
		runner:             runner,
		metricsOpts:        opts,
		connectionResolver: newConnectionResolver("default", defaultRunners, defaultLoaders, nil),
	}
}

// ValidateQuery checks SQL safety for the given connection without executing it.
func (s *QueriesService) ValidateQuery(ctx context.Context, connectionID *string, sql string) error {
	sql = strings.TrimSpace(sql)
	if sql == "" {
		return nil
	}
	runner, err := s.connectionResolver.runnerFor(connectionID)
	if err != nil {
		return err
	}
	return runner.ValidateQueryWithContext(ctx, sql)
}

// NewQueriesServiceWithEmbedding is like NewQueriesService but enables storing embeddings
// when saved queries are created, for similar-query retrieval and RAG. embeddingModel
// is the name of the embedding model (e.g. nomic-embed-text).
func NewQueriesServiceWithEmbedding(readOnlyPool *pgxpool.Pool, appPool db.DB, runner *queryrunner.Runner, metricsCfg config.MetricsConfig, embedder embedding.Embedder, embeddingStore *embedding.Store, embeddingModel string) *QueriesService {
	defaultRunners := map[string]*queryrunner.Runner{"default": runner}
	defaultLoaders := map[string]*catalog.Loader{}
	opts := metricsOptionsFromConfig(metricsCfg)
	return &QueriesService{
		readOnlyPool:       readOnlyPool,
		appPool:            appPool,
		runner:             runner,
		metricsOpts:        opts,
		embedder:           embedder,
		embeddingStore:     embeddingStore,
		embeddingModel:     embeddingModel,
		connectionResolver: newConnectionResolver("default", defaultRunners, defaultLoaders, nil),
	}
}

// NewQueriesServiceMultiConnection creates a queries service with one runner per connection.
func NewQueriesServiceMultiConnection(appPool db.DB, runners map[string]*queryrunner.Runner, defaultConnectionID string, metricsCfg config.MetricsConfig, embedder embedding.Embedder, embeddingStore *embedding.Store, embeddingModel string, readonlyUsers map[string]string) *QueriesService {
	var defaultRunner *queryrunner.Runner
	if r, ok := runners[defaultConnectionID]; ok {
		defaultRunner = r
	} else {
		for _, r := range runners {
			defaultRunner = r
			break
		}
	}
	opts := metricsOptionsFromConfig(metricsCfg)
	return &QueriesService{
		appPool:               appPool,
		runner:                defaultRunner,
		metricsOpts:           opts,
		embedder:              embedder,
		embeddingStore:        embeddingStore,
		embeddingModel:        embeddingModel,
		statStatementsEnabled: true,
		connectionResolver:    newConnectionResolver(defaultConnectionID, runners, map[string]*catalog.Loader{}, readonlyUsers),
	}
}

// Run executes a SQL query and returns the results.
//
// The query is validated, executed with timeout protection, and results are
// limited to prevent memory exhaustion. Errors are converted to appropriate
// API error types.
//
// Parameters:
//   - ctx: Context for cancellation and timeout
//   - payload: Query execution request (SQL and limit)
//
// Returns:
//   - RunQueryResult with columns, rows, and metadata
//   - ValidationError if query is invalid or times out
func (s *QueriesService) Run(ctx context.Context, payload *queries.RunQueryPayload) (*queries.RunQueryResult, error) {
	connID, err := s.connectionResolver.resolveConnectionID(payload.ConnectionID)
	if err != nil {
		return nil, connectionNotFoundQueriesError(err)
	}
	if err := checkConnectionAccess(ctx, s.authz, connID, auth.ActionQuery); err != nil {
		return nil, connectionForbiddenQueriesError(err)
	}
	runner, err := s.connectionResolver.runnerFor(payload.ConnectionID)
	if err != nil {
		return nil, connectionNotFoundQueriesError(err)
	}
	result, err := runner.Run(ctx, payload.SQL, int(payload.Limit))
	if err != nil {
		kind, userMsg := ClassifyRunError(err)
		if kind == RunErrorTimeout {
			apilog.ValidationError("run", "timeout_error", err.Error())
			return nil, &queries.ValidationError{Name: "timeout_error", Message: userMsg, Code: strPtr("TIMEOUT_ERROR")}
		}
		if kind == RunErrorTooLarge {
			apilog.ValidationError("run", "query_result_too_large", err.Error())
			return nil, &queries.ValidationError{Name: "query_result_too_large", Message: userMsg, Code: strPtr("QUERY_RESULT_TOO_LARGE")}
		}
		apilog.ValidationError("run", "validation_error", err.Error())
		return nil, &queries.ValidationError{Name: "validation_error", Message: userMsg, Code: strPtr("VALIDATION_ERROR")}
	}

	cols := make([]*queries.ColumnInfo, 0, len(result.Columns))
	colNames := make([]string, len(result.Columns))
	colTypes := make([]string, len(result.Columns))
	for i, col := range result.Columns {
		cols = append(cols, &queries.ColumnInfo{
			Name: col.Name,
			Type: col.Type,
		})
		colNames[i] = col.Name
		colTypes[i] = col.Type
	}

	chartSuggestions := suggestToQueries(charts.Suggest(colNames, colTypes, result.Rows))
	// The result is complete when it was not truncated by the row cap; in that
	// case period comparison can be computed in memory without re-running the query.
	resultComplete := result.RowCount < result.RowLimitApplied
	periodComparison, currentLabel, previousLabel := s.periodComparison(ctx, runner, payload.SQL, colNames, result.Rows, resultComplete)

	res := &queries.RunQueryResult{
		Columns:          cols,
		Rows:             result.Rows,
		RowCount:         clampInt32(result.RowCount),
		ExecutionTimeMs:  result.ExecutionTimeMs,
		Limit:            clampInt32(result.RowLimitApplied),
		ChartSuggestions: chartSuggestions,
		PeriodComparison: periodComparison,
	}
	if currentLabel != "" {
		res.PeriodCurrentLabel = &currentLabel
	}
	if previousLabel != "" {
		res.PeriodPreviousLabel = &previousLabel
	}
	observability.IncQueryRun()
	return res, nil
}

// ExplainPlan runs EXPLAIN (FORMAT JSON) on a read-only query and returns plan analysis.
func (s *QueriesService) ExplainPlan(ctx context.Context, payload *queries.ExplainQueryPayload) (*queries.ExplainQueryResult, error) {
	connID, err := s.connectionResolver.resolveConnectionID(payload.ConnectionID)
	if err != nil {
		return nil, connectionNotFoundQueriesError(err)
	}
	explainAction := auth.ActionExplain
	if payload.Analyze {
		explainAction = auth.ActionAnalyze
	}
	if err := checkConnectionAccess(ctx, s.authz, connID, explainAction); err != nil {
		return nil, connectionForbiddenQueriesError(err)
	}
	runner, err := s.connectionResolver.runnerFor(payload.ConnectionID)
	if err != nil {
		return nil, connectionNotFoundQueriesError(err)
	}
	result, err := runner.Explain(ctx, payload.SQL, payload.Analyze)
	if err != nil {
		kind, userMsg := ClassifyRunError(err)
		if kind == RunErrorTimeout {
			apilog.ValidationError("explain_plan", "timeout_error", err.Error())
			return nil, &queries.ValidationError{Name: "timeout_error", Message: userMsg, Code: strPtr("TIMEOUT_ERROR")}
		}
		if kind == RunErrorTooLarge {
			apilog.ValidationError("explain_plan", "query_result_too_large", err.Error())
			return nil, &queries.ValidationError{Name: "query_result_too_large", Message: userMsg, Code: strPtr("QUERY_RESULT_TOO_LARGE")}
		}
		apilog.ValidationError("explain_plan", "validation_error", err.Error())
		return nil, &queries.ValidationError{Name: "validation_error", Message: userMsg, Code: strPtr("VALIDATION_ERROR")}
	}

	findings := make([]*queries.PlanFinding, 0, len(result.Findings))
	for _, f := range result.Findings {
		findings = append(findings, planFindingToAPI(f))
	}

	var plan any
	if len(result.Plan) > 0 {
		_ = json.Unmarshal(result.Plan, &plan)
	}

	s.persistExplainSnapshot(ctx, ptrString(payload.ConnectionID), payload.Analyze, result)

	return &queries.ExplainQueryResult{
		SQL:             result.SQL,
		TotalCost:       result.TotalCost,
		Plan:            plan,
		Findings:        findings,
		ExecutionTimeMs: result.ExecutionTimeMs,
	}, nil
}

// ProjectIndexCost estimates plan cost if candidate DDL were applied (hypopg when
// available; otherwise an honest heuristic). Never executes real DDL.
func (s *QueriesService) ProjectIndexCost(ctx context.Context, connectionID *string, sql, candidateDDL string, baselineCost float64) queryrunner.IndexProjection {
	runner, err := s.connectionResolver.runnerFor(connectionID)
	if err != nil || runner == nil {
		return queryrunner.IndexProjection{
			Method:        queryrunner.IndexProjectionNone,
			BaselineCost:  baselineCost,
			ProjectedCost: baselineCost,
			Available:     false,
			Rationale:     "index cost projection unavailable (no runner)",
		}
	}
	return runner.ProjectIndexCost(ctx, sql, candidateDDL, baselineCost)
}

// StatStatements returns top queries from pg_stat_statements for observability.
func (s *QueriesService) StatStatements(ctx context.Context, payload *queries.StatStatementsPayload) (*queries.StatStatementsResult, error) {
	if !s.statStatementsEnabled {
		return nil, &queries.ValidationError{Name: "validation_error", Message: apperrors.ErrStatStatementsUnavailable.Error(), Code: strPtr("STAT_STATEMENTS_UNAVAILABLE")}
	}
	connID, err := s.connectionResolver.resolveConnectionID(payload.ConnectionID)
	if err != nil {
		return nil, connectionNotFoundQueriesError(err)
	}
	if err := checkConnectionAccess(ctx, s.authz, connID, auth.ActionStats); err != nil {
		return nil, connectionForbiddenQueriesError(err)
	}
	runner, err := s.connectionResolver.runnerFor(payload.ConnectionID)
	if err != nil {
		return nil, connectionNotFoundQueriesError(err)
	}
	filterRole, err := s.connectionResolver.readOnlyUserFor(payload.ConnectionID)
	if err != nil {
		return nil, connectionNotFoundQueriesError(err)
	}
	statsPool := runner.StatsPoolFor(ctx)
	if statsPool == nil {
		return nil, &queries.ValidationError{Name: "validation_error", Message: apperrors.ErrStatStatementsUnavailable.Error(), Code: strPtr("STAT_STATEMENTS_UNAVAILABLE")}
	}
	// Prefer the live role of the resolved (possibly tenant) pool over catalog config.
	var liveRole string
	if qerr := statsPool.QueryRow(ctx, `SELECT current_user`).Scan(&liveRole); qerr == nil && strings.TrimSpace(liveRole) != "" {
		filterRole = liveRole
	}
	orderBy := payload.OrderBy
	if orderBy == "" {
		orderBy = "total_time"
	}
	limit := int(payload.Limit)
	if limit == 0 {
		limit = 20
	}

	timeout := 30 * time.Second
	if runner != nil {
		timeout = runner.QueryTimeout()
	}
	result, err := queryrunner.StatStatements(ctx, statsPool, filterRole, orderBy, limit, timeout)
	if err != nil {
		apilog.ValidationError("stat_statements", "validation_error", err.Error())
		msg := SanitizeClientMessage(err)
		code := "VALIDATION_ERROR"
		if errors.Is(err, apperrors.ErrStatStatementsUnavailable) {
			code = "STAT_STATEMENTS_UNAVAILABLE"
		}
		return nil, &queries.ValidationError{Name: "validation_error", Message: msg, Code: strPtr(code)}
	}

	items := make([]*queries.StatStatementRow, 0, len(result.Items))
	for _, row := range result.Items {
		item := &queries.StatStatementRow{
			Query:       row.Query,
			Calls:       row.Calls,
			TotalTimeMs: row.TotalTimeMs,
			MeanTimeMs:  row.MeanTimeMs,
			Rows:        row.Rows,
		}
		if row.QueryID != "" {
			item.Queryid = &row.QueryID
		}
		items = append(items, item)
	}

	return &queries.StatStatementsResult{
		Items:   items,
		OrderBy: result.OrderBy,
		Limit:   clampInt32(result.Limit),
	}, nil
}

// suggestToQueries converts charts.Suggestion slice to API type.
func suggestToQueries(in []charts.Suggestion) []*queries.ChartSuggestion {
	if len(in) == 0 {
		return nil
	}
	out := make([]*queries.ChartSuggestion, len(in))
	for i := range in {
		out[i] = &queries.ChartSuggestion{
			ChartType: in[i].ChartType,
			Label:     in[i].Label,
			Reason:    in[i].Reason,
		}
	}
	return out
}

// metricsOptionsFromConfig builds metrics.Options from config. Nil or zero config uses defaults.
func metricsOptionsFromConfig(c config.MetricsConfig) *metrics.Options {
	o := &metrics.Options{
		TrendThresholdPercent:    c.TrendThresholdPercent,
		AnomalySigma:             c.AnomalySigma,
		AnomalyMethod:            c.AnomalyMethod,
		TrendPeriods:             c.TrendPeriods,
		MovingAvgWindow:          c.MovingAvgWindow,
		ConfidenceLevel:          c.ConfidenceLevel,
		MinRowsForCorrelation:    c.MinRowsForCorrelation,
		SmoothingAlpha:           c.SmoothingAlpha,
		SmoothingBeta:            c.SmoothingBeta,
		MaxSeasonalLag:           c.MaxSeasonalLag,
		MinPeriodsForSeasonality: c.MinPeriodsForSeasonality,
		MaxTimeSeriesPeriods:     c.MaxTimeSeriesPeriods,
	}
	o.ApplyDefaults()
	return o
}

// periodComparison computes period-over-period changes for time-series results.
// When the full result set is already in memory (resultComplete), it is computed
// in-process to avoid re-executing a potentially expensive query. The SQL window
// function (LAG) path re-runs the query and is reserved for truncated results,
// where in-memory rows may not represent all periods.
func (s *QueriesService) periodComparison(ctx context.Context, runner *queryrunner.Runner, sql string, columnNames []string, rows [][]interface{}, resultComplete bool) ([]*queries.PeriodComparisonItem, string, string) {
	if len(rows) < 2 {
		return nil, "", ""
	}

	if resultComplete {
		if items, cur, prev := timeSeriesToPeriodComparisonFallback(columnNames, rows, s.metricsOpts); len(items) > 0 {
			return items, cur, prev
		}
	}

	profiles := metrics.ProfileColumns(columnNames, rows)
	timeCol, measureCols := queryrunner.PeriodColumnsFromProfiles(columnNames, profiles)
	if timeCol != "" && len(measureCols) > 0 {
		threshold := s.metricsOpts.TrendThresholdPercent
		if out, err := runner.PeriodComparison(ctx, sql, timeCol, measureCols, threshold); err == nil && out != nil {
			return periodComparisonToAPI(out), out.CurrentPeriodLabel, out.PreviousPeriodLabel
		}
	}

	return timeSeriesToPeriodComparisonFallback(columnNames, rows, s.metricsOpts)
}

func periodComparisonToAPI(out *queryrunner.PeriodComparisonOutput) []*queries.PeriodComparisonItem {
	items := make([]*queries.PeriodComparisonItem, 0, len(out.Comparisons))
	for _, pc := range out.Comparisons {
		item := &queries.PeriodComparisonItem{
			Measure: pc.Measure,
			Current: pc.Current,
			Trend:   pc.Trend,
		}
		if pc.Previous != nil {
			item.Previous = pc.Previous
		}
		if pc.Change != nil {
			item.Change = pc.Change
		}
		if pc.ChangePercentage != nil {
			item.ChangePercentage = pc.ChangePercentage
		}
		items = append(items, item)
	}
	return items
}

// timeSeriesToPeriodComparisonFallback computes period-over-period in Go from raw result rows.
// Used when SQL LAG comparison is unavailable; full report metrics still use CalculateMetrics.
func timeSeriesToPeriodComparisonFallback(columnNames []string, rows [][]interface{}, opts *metrics.Options) ([]*queries.PeriodComparisonItem, string, string) {
	if len(rows) < 2 {
		return nil, "", ""
	}
	profiles := metrics.ProfileColumns(columnNames, rows)
	m := metrics.CalculateMetrics(columnNames, rows, profiles, opts)
	if len(m.TimeSeries) == 0 {
		return nil, "", ""
	}
	out := make([]*queries.PeriodComparisonItem, 0, len(m.TimeSeries))
	for measure, ts := range m.TimeSeries {
		item := &queries.PeriodComparisonItem{
			Measure: measure,
			Current: ts.CurrentPeriod,
			Trend:   ts.Trend,
		}
		if ts.PreviousPeriod != nil {
			item.Previous = ts.PreviousPeriod
		}
		if ts.Change != nil {
			item.Change = ts.Change
		}
		if ts.ChangePercentage != nil {
			item.ChangePercentage = ts.ChangePercentage
		}
		out = append(out, item)
	}
	return out, m.CurrentPeriodLabel, m.PreviousPeriodLabel
}

func connectionNotFoundQueriesError(err error) error {
	if err == nil {
		return nil
	}
	return &queries.ValidationError{
		Name:    "validation_error",
		Message: "connection not found",
		Code:    strPtr("CONNECTION_NOT_FOUND"),
	}
}

// connectionForbiddenQueriesError converts a connection-authorization denial
// (app/auth.ConnectionAuthorizer) into the queries API error type.
func connectionForbiddenQueriesError(err error) error {
	if err == nil {
		return nil
	}
	return &queries.ValidationError{
		Name:    "validation_error",
		Message: "connection access denied",
		Code:    strPtr("CONNECTION_FORBIDDEN"),
	}
}

// Save stores a query for later reuse.
//
// Parameters:
//   - ctx: Context for cancellation
//   - payload: Query to save (name, SQL, description, tags)
//
// Returns:
//   - SavedQuery with generated ID and timestamps
//   - Error if database operation fails
func (s *QueriesService) Save(ctx context.Context, payload *queries.SaveQueryPayload) (*queries.SavedQuery, error) {
	connectionID, err := s.resolveConnectionID(payload.ConnectionID)
	if err != nil {
		return nil, connectionNotFoundQueriesError(err)
	}
	if err := checkConnectionAccess(ctx, s.authz, connectionID, auth.ActionQuery); err != nil {
		return nil, connectionForbiddenQueriesError(err)
	}
	sqlAtRest, sealErr := sealProductSQL(s.dataEncKey, payload.SQL)
	if sealErr != nil {
		return nil, &queries.ValidationError{Name: "validation_error", Message: "failed to protect query SQL at rest", Code: strPtr("ENCRYPTION_ERROR")}
	}
	row := s.appPool.QueryRow(ctx, `
		INSERT INTO app.saved_queries (name, sql, description, tags, connection_id, organization_id, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, name, sql, description, tags, connection_id, created_at, updated_at
	`, payload.Name, sqlAtRest, payload.Description, payload.Tags, connectionID, orgID(ctx), auth.PrincipalFromContext(ctx).UserID)

	var item queries.SavedQuery
	var createdAt time.Time
	var updatedAt time.Time
	if err := row.Scan(&item.ID, &item.Name, &item.SQL, &item.Description, &item.Tags, &item.ConnectionID, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	item.SQL = openProductSQL(s.dataEncKey, item.SQL)
	item.CreatedAt = createdAt.Format(time.RFC3339)
	item.UpdatedAt = updatedAt.Format(time.RFC3339)

	// Optionally store embedding for similar-query retrieval and RAG
	if s.embedder != nil && s.embeddingStore != nil && s.embeddingModel != "" {
		text := item.Name
		if item.Description != nil && *item.Description != "" {
			text = text + " " + *item.Description
		}
		text = text + " " + item.SQL
		vec, err := s.embedder.Embed(embedding.WithOperation(ctx, "embed_query_save"), text)
		if err == nil {
			_ = s.embeddingStore.Upsert(ctx, item.ID, vec, s.embeddingModel)
		}
	}

	return &item, nil
}

// ListSaved retrieves a paginated list of saved queries.
//
// Supports optional filtering by tags. Results are ordered by creation date (newest first).
//
// Parameters:
//   - ctx: Context for cancellation
//   - payload: Pagination and optional tag filter
//
// Returns:
//   - SavedQueryList with items, limit, and offset
//   - Error if database operation fails
func (s *QueriesService) ListSaved(ctx context.Context, payload *queries.ListSavedPayload) (*queries.SavedQueryList, error) {
	limit := int(payload.Limit)
	offset := int(payload.Offset)
	p := auth.PrincipalFromContext(ctx)
	oid := p.OrgID
	if oid == "" {
		oid = orgID(ctx)
	}
	visPred := visibleResourcePredicate(1, 2, p.Role)

	var rows pgx.Rows
	var err error
	if len(payload.Tags) > 0 {
		if payload.ConnectionID != nil && strings.TrimSpace(*payload.ConnectionID) != "" {
			rows, err = s.appPool.Query(ctx, `
			SELECT id, name, sql, description, tags, connection_id, created_at, updated_at
			FROM app.saved_queries
			WHERE tags && $3 AND connection_id = $4 AND `+visPred+`
			ORDER BY created_at DESC
			LIMIT $5 OFFSET $6
		`, oid, p.UserID, payload.Tags, *payload.ConnectionID, limit, offset)
		} else {
			rows, err = s.appPool.Query(ctx, `
			SELECT id, name, sql, description, tags, connection_id, created_at, updated_at
			FROM app.saved_queries
			WHERE tags && $3 AND `+visPred+`
			ORDER BY created_at DESC
			LIMIT $4 OFFSET $5
		`, oid, p.UserID, payload.Tags, limit, offset)
		}
	} else {
		if payload.ConnectionID != nil && strings.TrimSpace(*payload.ConnectionID) != "" {
			rows, err = s.appPool.Query(ctx, `
			SELECT id, name, sql, description, tags, connection_id, created_at, updated_at
			FROM app.saved_queries
			WHERE connection_id = $3 AND `+visPred+`
			ORDER BY created_at DESC
			LIMIT $4 OFFSET $5
		`, oid, p.UserID, *payload.ConnectionID, limit, offset)
		} else {
			rows, err = s.appPool.Query(ctx, `
			SELECT id, name, sql, description, tags, connection_id, created_at, updated_at
			FROM app.saved_queries
			WHERE `+visPred+`
			ORDER BY created_at DESC
			LIMIT $3 OFFSET $4
		`, oid, p.UserID, limit, offset)
		}
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]*queries.SavedQuery, 0, limit)
	for rows.Next() {
		var item queries.SavedQuery
		var createdAt time.Time
		var updatedAt time.Time
		if err := rows.Scan(&item.ID, &item.Name, &item.SQL, &item.Description, &item.Tags, &item.ConnectionID, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		item.SQL = openProductSQL(s.dataEncKey, item.SQL)
		item.CreatedAt = createdAt.Format(time.RFC3339)
		item.UpdatedAt = updatedAt.Format(time.RFC3339)
		items = append(items, &item)
	}
	if rows.Err() != nil {
		return nil, rows.Err()
	}

	return &queries.SavedQueryList{
		Items:  items,
		Limit:  payload.Limit,
		Offset: payload.Offset,
	}, nil
}

// GetSaved retrieves a saved query by ID.
//
// Parameters:
//   - ctx: Context for cancellation
//   - payload: Query ID to retrieve
//
// Returns:
//   - SavedQuery if found
//   - NotFoundError if query doesn't exist
//   - Error if database operation fails
func (s *QueriesService) GetSaved(ctx context.Context, payload *queries.GetSavedPayload) (*queries.SavedQuery, error) {
	p := auth.PrincipalFromContext(ctx)
	visPred := visibleResourcePredicate(2, 3, p.Role)
	row := s.appPool.QueryRow(ctx, `
		SELECT id, name, sql, description, tags, connection_id, created_at, updated_at
		FROM app.saved_queries
		WHERE id = $1 AND `+visPred+`
	`, payload.ID, p.OrgID, p.UserID)

	var item queries.SavedQuery
	var createdAt time.Time
	var updatedAt time.Time
	if err := row.Scan(&item.ID, &item.Name, &item.SQL, &item.Description, &item.Tags, &item.ConnectionID, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, &queries.NotFoundError{
				Name:    "not_found",
				Message: "saved query not found",
				Code:    strPtr("NOT_FOUND"),
			}
		}
		return nil, err
	}
	item.SQL = openProductSQL(s.dataEncKey, item.SQL)
	item.CreatedAt = createdAt.Format(time.RFC3339)
	item.UpdatedAt = updatedAt.Format(time.RFC3339)
	return &item, nil
}

// DeleteSaved removes a saved query by ID.
//
// Parameters:
//   - ctx: Context for cancellation
//   - payload: Query ID to delete
//
// Returns:
//   - nil if deletion successful
//   - NotFoundError if query doesn't exist
//   - Error if database operation fails
func (s *QueriesService) DeleteSaved(ctx context.Context, payload *queries.DeleteSavedPayload) error {
	p := auth.PrincipalFromContext(ctx)
	var createdBy string
	err := s.appPool.QueryRow(ctx, `
		SELECT COALESCE(created_by, '') FROM app.saved_queries
		WHERE id = $1 AND organization_id = $2
	`, payload.ID, p.OrgID).Scan(&createdBy)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return &queries.NotFoundError{
				Name:    "not_found",
				Message: "saved query not found",
				Code:    strPtr("NOT_FOUND"),
			}
		}
		return err
	}
	if !canMutateOwnedResource(ctx, createdBy) {
		return &queries.NotFoundError{
			Name:    "not_found",
			Message: "saved query not found",
			Code:    strPtr("NOT_FOUND"),
		}
	}
	tag, err := s.appPool.Exec(ctx, `DELETE FROM app.saved_queries WHERE id = $1 AND organization_id = $2`, payload.ID, p.OrgID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return &queries.NotFoundError{
			Name:    "not_found",
			Message: "saved query not found",
			Code:    strPtr("NOT_FOUND"),
		}
	}
	return nil
}

func (s *QueriesService) persistExplainSnapshot(ctx context.Context, connectionID string, analyze bool, result *queryrunner.ExplainResult) {
	if s == nil || s.appPool == nil || result == nil {
		return
	}
	p := auth.PrincipalFromContext(ctx)
	findingsJSON, _ := json.Marshal(result.Findings)
	// sql_hash is computed from the original (unredacted) SQL so identical queries still
	// dedupe/correlate; only the persisted sql_text is redacted, since snapshots are
	// retained for later review/export and should not carry literal values (potential PII
	// or secrets embedded in query predicates) at rest. Constants are redacted via the
	// real PostgreSQL parser (AST-based) so numeric, boolean, and string literals are all
	// replaced with $n placeholders; if the statement cannot be parsed for any reason we
	// fall back to the coarser regex-based string-literal redaction rather than persisting
	// raw SQL.
	hash := sha256.Sum256([]byte(result.SQL))
	redactedSQL, ok := queryrunner.RedactConstants(result.SQL)
	if !ok {
		redactedSQL = llm.RedactSQL(result.SQL)
	}
	connID := strings.TrimSpace(connectionID)
	if connID == "" {
		connID = "default"
	}
	var planJSON json.RawMessage
	storageClass := SQLClassRedacted
	sqlText := redactedSQL
	if result.Plan != nil {
		if raw, err := json.Marshal(result.Plan); err == nil {
			var parsed interface{}
			if json.Unmarshal(raw, &parsed) == nil {
				planJSON = redactExplainPlanJSON(parsed)
			}
		}
	}
	if len(s.dataEncKey) > 0 {
		sealedSQL, serr := security.Seal(s.dataEncKey, redactedSQL)
		if serr != nil {
			return // fail closed: do not persist plaintext EXPLAIN snapshot when encryption is configured
		}
		sqlText = sealedSQL
		storageClass = SQLClassEncrypted
		if len(planJSON) > 0 {
			sealedPlan, perr := security.Seal(s.dataEncKey, string(planJSON))
			if perr != nil {
				return
			}
			encPayload, _ := json.Marshal(map[string]string{"ciphertext": sealedPlan})
			planJSON = encPayload
		}
	}
	_, _ = s.appPool.Exec(ctx, `
		INSERT INTO app.explain_snapshots (
			organization_id, user_id, connection_id, sql_hash, sql_text, used_analyze,
			total_cost, findings, explain_plan, execution_time_ms, sql_storage_class
		) VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`, p.OrgID, p.UserID, connID, hex.EncodeToString(hash[:]), sqlText, analyze,
		result.TotalCost, findingsJSON, planJSON, result.ExecutionTimeMs, string(storageClass))
}

// CleanupExplainSnapshots deletes stored EXPLAIN snapshots older than olderThan across all
// organizations, bounding how long redacted SQL/plan history is retained at rest. Runs in a
// scheduler-bypass transaction (see db.BeginSchedulerTx) since this is a cross-org background
// job, not a request scoped to a single organization. Returns the number of rows removed.
func CleanupExplainSnapshots(ctx context.Context, pool *pgxpool.Pool, olderThan time.Duration) (int64, error) {
	if pool == nil || olderThan <= 0 {
		return 0, nil
	}
	tx, err := db.BeginSchedulerTx(ctx, pool)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `
		DELETE FROM app.explain_snapshots WHERE created_at < NOW() - $1::interval
	`, fmt.Sprintf("%d seconds", int64(olderThan.Seconds())))
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// StartExplainSnapshotCleanupLoop runs CleanupExplainSnapshots on interval until ctx is done.
// Safe to call at most once per process; retentionDays <= 0 disables the loop (snapshots are
// kept forever).
func StartExplainSnapshotCleanupLoop(ctx context.Context, pool *pgxpool.Pool, interval time.Duration, retentionDays int) {
	if pool == nil || interval <= 0 || retentionDays <= 0 {
		return
	}
	retention := time.Duration(retentionDays) * 24 * time.Hour
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := CleanupExplainSnapshots(ctx, pool, retention); err != nil {
					apilog.ValidationError("explain_snapshot_cleanup", "cleanup_error", err.Error())
				}
			}
		}
	}()
}
