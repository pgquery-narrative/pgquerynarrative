// Package service provides business logic for queries and reports.
// It acts as a bridge between the API layer and the data/query execution layer.
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
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
	"github.com/pgquerynarrative/pgquerynarrative/app/metrics"
	"github.com/pgquerynarrative/pgquerynarrative/app/observability"
	"github.com/pgquerynarrative/pgquerynarrative/app/queryrunner"
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
}

// SetStatStatementsEnabled toggles the pg_stat_statements API.
func (s *QueriesService) SetStatStatementsEnabled(enabled bool) {
	s.statStatementsEnabled = enabled
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
func (s *QueriesService) ValidateQuery(connectionID *string, sql string) error {
	sql = strings.TrimSpace(sql)
	if sql == "" {
		return nil
	}
	return s.connectionResolver.runnerFor(connectionID).ValidateQuery(sql)
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
	runner := s.connectionResolver.runnerFor(payload.ConnectionID)
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

	var rowCount32 int32 = math.MaxInt32
	if result.RowCount < math.MaxInt32 {
		rowCount32 = int32(result.RowCount)
	}
	res := &queries.RunQueryResult{
		Columns:          cols,
		Rows:             result.Rows,
		RowCount:         rowCount32,
		ExecutionTimeMs:  result.ExecutionTimeMs,
		Limit:            int32(result.RowLimitApplied),
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
	runner := s.connectionResolver.runnerFor(payload.ConnectionID)
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
		cost := f.EstimatedCost
		pf := &queries.PlanFinding{
			NodeType:      f.NodeType,
			EstimatedCost: &cost,
			IsSeqScan:     f.IsSeqScan,
			Message:       f.Message,
		}
		if f.Schema != "" {
			pf.Schema = &f.Schema
		}
		if f.Relation != "" {
			pf.Relation = &f.Relation
		}
		if f.Category != "" {
			pf.Category = &f.Category
		}
		if f.Confidence != "" {
			pf.Confidence = &f.Confidence
		}
		if len(f.Evidence) > 0 {
			pf.Evidence = f.Evidence
		}
		findings = append(findings, pf)
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

// StatStatements returns top queries from pg_stat_statements for observability.
func (s *QueriesService) StatStatements(ctx context.Context, payload *queries.StatStatementsPayload) (*queries.StatStatementsResult, error) {
	if !s.statStatementsEnabled {
		return nil, &queries.ValidationError{Name: "validation_error", Message: apperrors.ErrStatStatementsUnavailable.Error(), Code: strPtr("VALIDATION_ERROR")}
	}
	runner := s.connectionResolver.runnerFor(payload.ConnectionID)
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
	result, err := queryrunner.StatStatements(ctx, s.appPool, s.connectionResolver.readOnlyUserFor(payload.ConnectionID), orderBy, limit, timeout)
	if err != nil {
		apilog.ValidationError("stat_statements", "validation_error", err.Error())
		msg := SanitizeClientMessage(err)
		return nil, &queries.ValidationError{Name: "validation_error", Message: msg, Code: strPtr("VALIDATION_ERROR")}
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
		Limit:   int32(result.Limit),
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
	connectionID := s.normalizedConnectionID(payload.ConnectionID)
	row := s.appPool.QueryRow(ctx, `
		INSERT INTO app.saved_queries (name, sql, description, tags, connection_id, organization_id, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, name, sql, description, tags, connection_id, created_at, updated_at
	`, payload.Name, payload.SQL, payload.Description, payload.Tags, connectionID, orgID(ctx), auth.PrincipalFromContext(ctx).UserID)

	var item queries.SavedQuery
	var createdAt time.Time
	var updatedAt time.Time
	if err := row.Scan(&item.ID, &item.Name, &item.SQL, &item.Description, &item.Tags, &item.ConnectionID, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	item.CreatedAt = createdAt.Format(time.RFC3339)
	item.UpdatedAt = updatedAt.Format(time.RFC3339)

	// Optionally store embedding for similar-query retrieval and RAG
	if s.embedder != nil && s.embeddingStore != nil && s.embeddingModel != "" {
		text := item.Name
		if item.Description != nil && *item.Description != "" {
			text = text + " " + *item.Description
		}
		text = text + " " + item.SQL
		vec, err := s.embedder.Embed(ctx, text)
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
	oid := orgID(ctx)

	var rows pgx.Rows
	var err error
	if len(payload.Tags) > 0 {
		if payload.ConnectionID != nil && strings.TrimSpace(*payload.ConnectionID) != "" {
			rows, err = s.appPool.Query(ctx, `
			SELECT id, name, sql, description, tags, connection_id, created_at, updated_at
			FROM app.saved_queries
			WHERE tags && $1 AND connection_id = $2 AND organization_id = $3
			ORDER BY created_at DESC
			LIMIT $4 OFFSET $5
		`, payload.Tags, *payload.ConnectionID, oid, limit, offset)
		} else {
			rows, err = s.appPool.Query(ctx, `
			SELECT id, name, sql, description, tags, connection_id, created_at, updated_at
			FROM app.saved_queries
			WHERE tags && $1 AND organization_id = $2
			ORDER BY created_at DESC
			LIMIT $3 OFFSET $4
		`, payload.Tags, oid, limit, offset)
		}
	} else {
		if payload.ConnectionID != nil && strings.TrimSpace(*payload.ConnectionID) != "" {
			rows, err = s.appPool.Query(ctx, `
			SELECT id, name, sql, description, tags, connection_id, created_at, updated_at
			FROM app.saved_queries
			WHERE connection_id = $1 AND organization_id = $2
			ORDER BY created_at DESC
			LIMIT $3 OFFSET $4
		`, *payload.ConnectionID, oid, limit, offset)
		} else {
			rows, err = s.appPool.Query(ctx, `
			SELECT id, name, sql, description, tags, connection_id, created_at, updated_at
			FROM app.saved_queries
			WHERE organization_id = $1
			ORDER BY created_at DESC
			LIMIT $2 OFFSET $3
		`, oid, limit, offset)
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
	row := s.appPool.QueryRow(ctx, `
		SELECT id, name, sql, description, tags, connection_id, created_at, updated_at
		FROM app.saved_queries
		WHERE id = $1 AND organization_id = $2
	`, payload.ID, orgID(ctx))

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
	tag, err := s.appPool.Exec(ctx, `DELETE FROM app.saved_queries WHERE id = $1 AND organization_id = $2`, payload.ID, orgID(ctx))
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
	hash := sha256.Sum256([]byte(result.SQL))
	connID := strings.TrimSpace(connectionID)
	if connID == "" {
		connID = "default"
	}
	_, _ = s.appPool.Exec(ctx, `
		INSERT INTO app.explain_snapshots (
			organization_id, user_id, connection_id, sql_hash, sql_text, used_analyze,
			total_cost, findings, explain_plan, execution_time_ms
		) VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, p.OrgID, p.UserID, connID, hex.EncodeToString(hash[:]), result.SQL, analyze,
		result.TotalCost, findingsJSON, result.Plan, result.ExecutionTimeMs)
}
