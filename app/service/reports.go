package service

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/api/gen/reports"
	"github.com/pgquerynarrative/pgquerynarrative/app/apilog"
	"github.com/pgquerynarrative/pgquerynarrative/app/charts"
	"github.com/pgquerynarrative/pgquerynarrative/app/config"
	"github.com/pgquerynarrative/pgquerynarrative/app/debuglog"
	"github.com/pgquerynarrative/pgquerynarrative/app/embedding"
	"github.com/pgquerynarrative/pgquerynarrative/app/llm"
	"github.com/pgquerynarrative/pgquerynarrative/app/metrics"
	"github.com/pgquerynarrative/pgquerynarrative/app/queryrunner"
	"github.com/pgquerynarrative/pgquerynarrative/app/story"
)

type ReportsService struct {
	readOnlyPool   *pgxpool.Pool
	appPool        *pgxpool.Pool
	runner         *queryrunner.Runner
	llmClient      llm.Client
	generator      *story.Generator
	metricsOpts    *metrics.Options
	embedder       embedding.Embedder
	embeddingStore *embedding.Store
	runners        map[string]*queryrunner.Runner
	connectionResolver
	shareLinkDefaultHours int
}

// reportGenOpts toggles optional stages inside report generation.
type reportGenOpts struct {
	// skipNarrativeLLM skips the story LLM and RAG/embedding work tied to narrative quality.
	// Used for Ask + Ollama so only the NL→SQL call hits the local model (second call was often minutes or appeared hung).
	skipNarrativeLLM bool
}

// SetPromptOptions configures LLM data governance for narrative generation.
func (s *ReportsService) SetPromptOptions(opts llm.PromptOptions) {
	if s.generator != nil {
		s.generator.SetPromptOptions(opts)
	}
}

// SetShareLinkDefaultHours sets the default TTL for share links when expires_in_hours is omitted.
func (s *ReportsService) SetShareLinkDefaultHours(hours int) {
	if hours > 0 {
		s.shareLinkDefaultHours = hours
	}
}

func NewReportsService(readOnlyPool, appPool *pgxpool.Pool, runner *queryrunner.Runner, llmClient llm.Client, metricsCfg config.MetricsConfig) *ReportsService {
	opts := metricsOptionsFromConfig(metricsCfg)
	return &ReportsService{
		readOnlyPool:       readOnlyPool,
		appPool:            appPool,
		runner:             runner,
		llmClient:          llmClient,
		generator:          story.NewGenerator(llmClient),
		metricsOpts:        opts,
		runners:            map[string]*queryrunner.Runner{"default": runner},
		connectionResolver: newConnectionResolver("default", map[string]*queryrunner.Runner{"default": runner}, nil, nil),
	}
}

// NewReportsServiceWithRAG is like NewReportsService but enables RAG: similar past
// queries are retrieved and added to the narrative prompt when generating reports.
func NewReportsServiceWithRAG(readOnlyPool, appPool *pgxpool.Pool, runner *queryrunner.Runner, llmClient llm.Client, metricsCfg config.MetricsConfig, embedder embedding.Embedder, embeddingStore *embedding.Store) *ReportsService {
	opts := metricsOptionsFromConfig(metricsCfg)
	return &ReportsService{
		readOnlyPool:       readOnlyPool,
		appPool:            appPool,
		runner:             runner,
		llmClient:          llmClient,
		generator:          story.NewGenerator(llmClient),
		metricsOpts:        opts,
		embedder:           embedder,
		embeddingStore:     embeddingStore,
		runners:            map[string]*queryrunner.Runner{"default": runner},
		connectionResolver: newConnectionResolver("default", map[string]*queryrunner.Runner{"default": runner}, nil, nil),
	}
}

// NewReportsServiceMultiConnection creates a reports service with one runner per connection.
func NewReportsServiceMultiConnection(appPool *pgxpool.Pool, runners map[string]*queryrunner.Runner, defaultConnectionID string, llmClient llm.Client, metricsCfg config.MetricsConfig, embedder embedding.Embedder, embeddingStore *embedding.Store) *ReportsService {
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
	return &ReportsService{
		appPool:            appPool,
		runner:             defaultRunner,
		llmClient:          llmClient,
		generator:          story.NewGenerator(llmClient),
		metricsOpts:        opts,
		embedder:           embedder,
		embeddingStore:     embeddingStore,
		runners:            runners,
		connectionResolver: newConnectionResolver(defaultConnectionID, runners, nil, nil),
	}
}

func (s *ReportsService) Generate(ctx context.Context, payload *reports.GenerateReportPayload) (*reports.Report, error) {
	return s.generateReport(ctx, payload, reportGenOpts{})
}

// GenerateForAsk is like Generate but, when the LLM provider is Ollama, skips the
// narrative LLM and embedding/RAG steps so Ask returns after a single local model
// round-trip (NL→SQL). Use Generate Report on the SQL for a full narrative.
func (s *ReportsService) GenerateForAsk(ctx context.Context, payload *reports.GenerateReportPayload) (*reports.Report, error) {
	opts := reportGenOpts{}
	if s.llmClient != nil && s.llmClient.Name() == "ollama" {
		opts.skipNarrativeLLM = true
	}
	return s.generateReport(ctx, payload, opts)
}

func (s *ReportsService) generateReport(ctx context.Context, payload *reports.GenerateReportPayload, opts reportGenOpts) (*reports.Report, error) {
	debuglog.Log("report generation started")
	// Execute query
	connectionID := s.normalizedConnectionID(payload.ConnectionID)
	queryResult, err := s.connectionResolver.runnerFor(payload.ConnectionID).Run(ctx, payload.SQL, 1000)
	if err != nil {
		kind, userMsg := ClassifyRunError(err)
		if kind == RunErrorTimeout {
			apilog.ValidationError("generate", "timeout_error", err.Error())
			return nil, &reports.ValidationError{Name: "timeout_error", Message: userMsg, Code: strPtr("TIMEOUT_ERROR")}
		}
		apilog.ValidationError("generate", "validation_error", err.Error())
		return nil, &reports.ValidationError{Name: "validation_error", Message: userMsg, Code: strPtr("VALIDATION_ERROR")}
	}

	// Extract column names and types
	columnNames := make([]string, len(queryResult.Columns))
	columnTypes := make([]string, len(queryResult.Columns))
	for i, col := range queryResult.Columns {
		columnNames[i] = col.Name
		columnTypes[i] = col.Type
	}

	// Rule-based chart suggestions from result shape (base ordering).
	ruleSuggestions := charts.Suggest(columnNames, columnTypes, queryResult.Rows)

	// Profile columns
	profiles := metrics.ProfileColumns(columnNames, queryResult.Rows)

	// Calculate metrics
	calcMetrics := metrics.CalculateMetrics(columnNames, queryResult.Rows, profiles, s.metricsOpts)
	calcMetrics.PerfSuggestions = BuildPerfSuggestions(queryResult)
	s.enrichTimeSeriesExplanations(ctx, calcMetrics)

	// Optional RAG: retrieve similar past queries and add to prompt context
	var similarContext string
	if !opts.skipNarrativeLLM && s.embedder != nil && s.embeddingStore != nil {
		if vec, err := s.embedder.Embed(ctx, payload.SQL); err == nil {
			if similar, err := s.embeddingStore.FindSimilar(ctx, vec, 3); err == nil && len(similar) > 0 {
				const maxSQLLen = 200
				var b strings.Builder
				for _, q := range similar {
					b.WriteString("- ")
					b.WriteString(q.Name)
					b.WriteString(": ")
					sql := q.SQL
					if len(sql) > maxSQLLen {
						sql = sql[:maxSQLLen] + "..."
					}
					b.WriteString(sql)
					b.WriteString("\n")
				}
				similarContext = strings.TrimSpace(b.String())
			}
		}
	}

	var narrative *story.NarrativeContent
	if opts.skipNarrativeLLM {
		debuglog.Log("skipping narrative LLM (Ask + Ollama fast path)")
		narrative = buildOllamaAskFastNarrative(queryResult.RowCount, calcMetrics.PerfSuggestions)
	} else {
		debuglog.Log("calling LLM for narrative generation")
		var errGen error
		narrative, errGen = s.generator.Generate(ctx, payload.SQL, columnNames, queryResult.Rows, calcMetrics, similarContext)
		if errGen != nil {
			llmMsg := errGen.Error()
			apilog.LLMError(llmMsg)
			narrative = buildFallbackNarrative(queryResult.RowCount, calcMetrics.PerfSuggestions)
		}
	}

	var finalSuggestions []charts.Suggestion
	if opts.skipNarrativeLLM {
		finalSuggestions = ruleSuggestions
	} else {
		finalSuggestions = s.applyLLMChartRecommendation(ctx, narrative.Headline, columnNames, columnTypes, queryResult.RowCount, ruleSuggestions)
	}
	chartSuggestions := suggestToReports(finalSuggestions)

	// Convert metrics to API format
	metricsData := ConvertMetrics(calcMetrics)
	providerName, modelName := llmMetadata(s.llmClient)

	// Store report in database
	debuglog.Log("storing report in database")
	reportID, err := s.storeReport(ctx, payload, narrative, calcMetrics, queryResult, providerName, modelName, connectionID)
	if err != nil {
		return nil, err
	}
	if !opts.skipNarrativeLLM {
		s.storeReportEmbedding(ctx, reportID, narrative, modelName)
	}
	apilog.Request("generate", "report_id="+reportID)

	// Convert narrative to API format
	narrativeData := &reports.NarrativeContent{
		Headline:        narrative.Headline,
		Takeaways:       narrative.Takeaways,
		Drivers:         narrative.Drivers,
		Limitations:     narrative.Limitations,
		Recommendations: narrative.Recommendations,
	}

	return &reports.Report{
		ID:               reportID,
		SavedQueryID:     payload.SavedQueryID,
		SQL:              payload.SQL,
		Narrative:        narrativeData,
		Metrics:          metricsData,
		ChartSuggestions: chartSuggestions,
		ConnectionID:     connectionID,
		CreatedAt:        time.Now().Format(time.RFC3339),
		LlmModel:         modelName,
		LlmProvider:      providerName,
	}, nil
}

func (s *ReportsService) enrichTimeSeriesExplanations(ctx context.Context, m *metrics.Metrics) {
	if s.llmClient == nil || m == nil || len(m.TimeSeries) == 0 {
		return
	}
	// Local Ollama: one HTTP generate per trend/anomaly would serialize to minutes and
	// overwhelm the server; main narrative already summarizes the metrics.
	if s.llmClient.Name() == "ollama" {
		return
	}
	for measure, ts := range m.TimeSeries {
		if ts.TrendSummary != nil {
			if explanation := s.generateTrendExplanation(ctx, measure, ts); explanation != "" {
				ts.TrendSummary.Explanation = explanation
			}
		}
		if len(ts.Anomalies) > 0 {
			for i := range ts.Anomalies {
				if explanation := s.generateAnomalyExplanation(ctx, measure, ts, i); explanation != "" {
					ts.Anomalies[i].Explanation = explanation
				}
			}
		}
		m.TimeSeries[measure] = ts
	}
}

func (s *ReportsService) generateTrendExplanation(ctx context.Context, measure string, ts metrics.TimeSeriesMetric) string {
	if ts.TrendSummary == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("Explain this time-series trend in exactly one sentence, under 28 words.\n")
	b.WriteString("Do not mention SQL or statistical jargon unless required.\n")
	b.WriteString("Measure: ")
	b.WriteString(measure)
	b.WriteString("\nDirection: ")
	b.WriteString(ts.TrendSummary.Direction)
	b.WriteString("\nSummary: ")
	b.WriteString(ts.TrendSummary.Summary)
	b.WriteString("\nRecent points:\n")
	for _, p := range tailPeriodPoints(ts.Periods, 5) {
		b.WriteString("- ")
		b.WriteString(p.Label)
		b.WriteString(": ")
		b.WriteString(formatFloatForPrompt(p.Value))
		b.WriteString("\n")
	}
	raw, err := s.llmClient.Generate(ctx, b.String())
	if err != nil {
		return ""
	}
	return normalizeOneSentence(raw)
}

func (s *ReportsService) generateAnomalyExplanation(ctx context.Context, measure string, ts metrics.TimeSeriesMetric, idx int) string {
	if idx < 0 || idx >= len(ts.Anomalies) {
		return ""
	}
	a := ts.Anomalies[idx]
	var b strings.Builder
	b.WriteString("Explain this anomaly in exactly one sentence, under 28 words.\n")
	b.WriteString("Use plain business language and mention likely context from nearby periods.\n")
	b.WriteString("Measure: ")
	b.WriteString(measure)
	b.WriteString("\nAnomaly period: ")
	b.WriteString(a.PeriodLabel)
	b.WriteString("\nAnomaly value: ")
	b.WriteString(formatFloatForPrompt(a.Value))
	b.WriteString("\nReason: ")
	b.WriteString(a.Reason)
	b.WriteString("\nNeighboring points:\n")
	for _, p := range neighboringPoints(ts.Periods, a.PeriodLabel, 2) {
		b.WriteString("- ")
		b.WriteString(p.Label)
		b.WriteString(": ")
		b.WriteString(formatFloatForPrompt(p.Value))
		b.WriteString("\n")
	}
	raw, err := s.llmClient.Generate(ctx, b.String())
	if err != nil {
		return ""
	}
	return normalizeOneSentence(raw)
}

func tailPeriodPoints(points []metrics.PeriodPoint, n int) []metrics.PeriodPoint {
	if n <= 0 || len(points) == 0 {
		return nil
	}
	if len(points) <= n {
		return points
	}
	return points[len(points)-n:]
}

func neighboringPoints(points []metrics.PeriodPoint, periodLabel string, radius int) []metrics.PeriodPoint {
	if len(points) == 0 {
		return nil
	}
	index := -1
	for i := range points {
		if points[i].Label == periodLabel {
			index = i
			break
		}
	}
	if index == -1 {
		return tailPeriodPoints(points, 5)
	}
	start := index - radius
	if start < 0 {
		start = 0
	}
	end := index + radius + 1
	if end > len(points) {
		end = len(points)
	}
	return points[start:end]
}

func normalizeOneSentence(raw string) string {
	text := strings.TrimSpace(raw)
	text = strings.TrimPrefix(text, "- ")
	text = strings.TrimLeft(text, "0123456789.) ")
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if i := strings.Index(text, "\n"); i >= 0 {
		text = strings.TrimSpace(text[:i])
	}
	if !strings.HasSuffix(text, ".") && !strings.HasSuffix(text, "!") && !strings.HasSuffix(text, "?") {
		text += "."
	}
	return text
}

func formatFloatForPrompt(v float64) string {
	return fmt.Sprintf("%.4g", v)
}

func strPtrIfNotEmpty(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return strPtr(s)
}

func (s *ReportsService) storeReportEmbedding(ctx context.Context, reportID string, narrative *story.NarrativeContent, modelName string) {
	if s.embedder == nil || s.embeddingStore == nil || reportID == "" || narrative == nil {
		return
	}
	text := strings.TrimSpace(narrative.Headline + "\n" + strings.Join(narrative.Takeaways, "\n") + "\n" + strings.Join(narrative.Drivers, "\n"))
	if text == "" {
		return
	}
	vec, err := s.embedder.Embed(ctx, text)
	if err != nil {
		return
	}
	_ = s.embeddingStore.UpsertReport(ctx, reportID, vec, modelName)
}

func buildFallbackNarrative(rowCount int, perfSuggestions []string) *story.NarrativeContent {
	n := &story.NarrativeContent{
		Headline: "Report generated without LLM narrative",
		Takeaways: []string{
			"Query executed successfully and returned " + strconv.Itoa(rowCount) + " rows.",
		},
		Limitations: []string{
			"Natural-language narrative is unavailable right now; showing metrics and raw results instead.",
		},
	}
	if len(perfSuggestions) > 0 {
		n.Recommendations = append(n.Recommendations, perfSuggestions...)
	}
	return n
}

// buildOllamaAskFastNarrative is used when Ask skips the narrative LLM for local Ollama.
func buildOllamaAskFastNarrative(rowCount int, perfSuggestions []string) *story.NarrativeContent {
	n := buildFallbackNarrative(rowCount, perfSuggestions)
	n.Headline = "Query ran — charts and metrics below"
	n.Limitations = append([]string{
		"Ask with Ollama skips the second LLM pass (full narrative) so the button returns quickly. Paste the SQL into the editor and click “Generate Report” for a full story, or use Groq/Gemini/OpenAI in .env for narrative on Ask.",
	}, n.Limitations...)
	return n
}

// suggestToReports converts charts.Suggestion slice to reports API type.
func suggestToReports(in []charts.Suggestion) []*reports.ChartSuggestion {
	if len(in) == 0 {
		return nil
	}
	out := make([]*reports.ChartSuggestion, len(in))
	for i := range in {
		out[i] = &reports.ChartSuggestion{
			ChartType: in[i].ChartType,
			Label:     in[i].Label,
			Reason:    in[i].Reason,
		}
	}
	return out
}

func (s *ReportsService) applyLLMChartRecommendation(ctx context.Context, headline string, columnNames, columnTypes []string, rowCount int, base []charts.Suggestion) []charts.Suggestion {
	if len(base) == 0 || s.llmClient == nil {
		return base
	}
	if s.llmClient.Name() == "ollama" {
		return base
	}
	prompt := buildChartRecommendationPrompt(headline, columnNames, columnTypes, rowCount, base)
	raw, err := s.llmClient.Generate(ctx, prompt)
	if err != nil {
		return base
	}
	chartType, reason := parseChartRecommendation(raw)
	if chartType == "" {
		return base
	}
	label := chartTypeLabel(chartType)
	if label == "" {
		return base
	}
	if strings.TrimSpace(reason) == "" {
		reason = "LLM recommended this chart to support the narrative emphasis."
	}
	// Move suggested chart to front if it already exists, otherwise prepend.
	out := make([]charts.Suggestion, 0, len(base)+1)
	out = append(out, charts.Suggestion{
		ChartType: chartType,
		Label:     label,
		Reason:    reason,
	})
	for _, s := range base {
		if s.ChartType == chartType {
			continue
		}
		out = append(out, s)
	}
	return out
}

func buildChartRecommendationPrompt(headline string, columnNames, columnTypes []string, rowCount int, base []charts.Suggestion) string {
	var b strings.Builder
	b.WriteString("You are selecting one best chart type for a report.\n")
	b.WriteString("Allowed chart_type values: bar, line, pie, area, table.\n")
	b.WriteString("Return STRICT JSON only with keys chart_type and reason.\n")
	b.WriteString(`Example: {"chart_type":"line","reason":"Shows time trend clearly."}` + "\n\n")
	b.WriteString("Narrative headline: ")
	b.WriteString(headline)
	b.WriteString("\nRow count: ")
	b.WriteString(strconv.Itoa(rowCount))
	b.WriteString("\nColumns:\n")
	for i := range columnNames {
		b.WriteString("- ")
		b.WriteString(columnNames[i])
		if i < len(columnTypes) {
			b.WriteString(" (")
			b.WriteString(columnTypes[i])
			b.WriteString(")")
		}
		b.WriteString("\n")
	}
	b.WriteString("Rule-based suggestions:\n")
	for _, s := range base {
		b.WriteString("- ")
		b.WriteString(s.ChartType)
		b.WriteString(": ")
		b.WriteString(s.Reason)
		b.WriteString("\n")
	}
	return b.String()
}

func parseChartRecommendation(raw string) (chartType, reason string) {
	type rec struct {
		ChartType string `json:"chart_type"`
		Reason    string `json:"reason"`
	}
	trimmed := strings.TrimSpace(raw)
	var r rec
	if err := json.Unmarshal([]byte(trimmed), &r); err == nil {
		return normalizeChartType(r.ChartType), strings.TrimSpace(r.Reason)
	}
	lower := strings.ToLower(trimmed)
	for _, t := range []string{"line", "bar", "pie", "area", "table"} {
		if strings.Contains(lower, t) {
			return t, strings.TrimSpace(trimmed)
		}
	}
	return "", ""
}

func normalizeChartType(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "bar", "line", "pie", "area", "table":
		return strings.ToLower(strings.TrimSpace(v))
	default:
		return ""
	}
}

func chartTypeLabel(chartType string) string {
	switch chartType {
	case "bar":
		return "Bar chart"
	case "line":
		return "Line chart"
	case "pie":
		return "Pie chart"
	case "area":
		return "Area chart"
	case "table":
		return "Table"
	default:
		return ""
	}
}

func (s *ReportsService) storeReport(ctx context.Context, payload *reports.GenerateReportPayload, narrative *story.NarrativeContent, calcMetrics *metrics.Metrics, queryResult *queryrunner.Result, providerName, modelName, connectionID string) (string, error) {
	narrativeJSON, _ := json.Marshal(narrative)
	metricsJSON, _ := json.Marshal(calcMetrics)
	statsJSON, _ := json.Marshal(map[string]interface{}{
		"execution_time_ms": queryResult.ExecutionTimeMs,
		"row_count":         queryResult.RowCount,
	})

	var reportID string
	err := s.appPool.QueryRow(ctx, `
		INSERT INTO app.reports (
			saved_query_id, sql, narrative_md, narrative_json, metrics, stats,
			llm_model, llm_provider, success, connection_id
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING id
	`, payload.SavedQueryID, payload.SQL, narrative.Headline, narrativeJSON, metricsJSON, statsJSON,
		modelName, providerName, true, connectionID).Scan(&reportID)

	return reportID, err
}

func llmMetadata(client llm.Client) (providerName, modelName string) {
	if client == nil {
		return "", ""
	}
	providerName = client.Name()
	modelName = providerName
	if modeler, ok := client.(llm.Modeler); ok {
		if model := strings.TrimSpace(modeler.Model()); model != "" {
			modelName = model
		}
	}
	return providerName, modelName
}

func (s *ReportsService) Get(ctx context.Context, payload *reports.GetPayload) (*reports.Report, error) {
	row := s.appPool.QueryRow(ctx, `
		SELECT id, saved_query_id, sql, narrative_json, metrics, created_at, llm_model, llm_provider, connection_id
		FROM app.reports
		WHERE id = $1
	`, payload.ID)

	var report reports.Report
	var savedQueryID sql.NullString
	var narrativeJSON []byte
	var metricsJSON []byte
	var createdAt time.Time

	err := row.Scan(&report.ID, &savedQueryID, &report.SQL, &narrativeJSON, &metricsJSON, &createdAt, &report.LlmModel, &report.LlmProvider, &report.ConnectionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, &reports.NotFoundError{
				Name:    "not_found",
				Message: "report not found",
				Code:    strPtr("NOT_FOUND"),
			}
		}
		return nil, err
	}

	if savedQueryID.Valid {
		report.SavedQueryID = &savedQueryID.String
	}

	var narrative story.NarrativeContent
	if err := json.Unmarshal(narrativeJSON, &narrative); err == nil {
		report.Narrative = &reports.NarrativeContent{
			Headline:        narrative.Headline,
			Takeaways:       narrative.Takeaways,
			Drivers:         narrative.Drivers,
			Limitations:     narrative.Limitations,
			Recommendations: narrative.Recommendations,
		}
	}

	var calcMetrics metrics.Metrics
	if err := json.Unmarshal(metricsJSON, &calcMetrics); err == nil {
		report.Metrics = ConvertMetrics(&calcMetrics)
	}

	report.CreatedAt = createdAt.Format(time.RFC3339)

	return &report, nil
}

func (s *ReportsService) List(ctx context.Context, payload *reports.ListPayload) (*reports.ReportList, error) {
	limit := int(payload.Limit)
	offset := int(payload.Offset)

	var rows pgx.Rows
	var err error

	if payload.SavedQueryID != nil && *payload.SavedQueryID != "" {
		if payload.ConnectionID != nil && *payload.ConnectionID != "" {
			rows, err = s.appPool.Query(ctx, `
			SELECT id, saved_query_id, sql, narrative_json, metrics, created_at, llm_model, llm_provider, connection_id
			FROM app.reports
			WHERE saved_query_id = $1 AND connection_id = $2
			ORDER BY created_at DESC
			LIMIT $3 OFFSET $4
		`, *payload.SavedQueryID, *payload.ConnectionID, limit, offset)
		} else {
			rows, err = s.appPool.Query(ctx, `
			SELECT id, saved_query_id, sql, narrative_json, metrics, created_at, llm_model, llm_provider, connection_id
			FROM app.reports
			WHERE saved_query_id = $1
			ORDER BY created_at DESC
			LIMIT $2 OFFSET $3
		`, *payload.SavedQueryID, limit, offset)
		}
	} else if payload.ConnectionID != nil && *payload.ConnectionID != "" {
		rows, err = s.appPool.Query(ctx, `
			SELECT id, saved_query_id, sql, narrative_json, metrics, created_at, llm_model, llm_provider, connection_id
			FROM app.reports
			WHERE connection_id = $1
			ORDER BY created_at DESC
			LIMIT $2 OFFSET $3
		`, *payload.ConnectionID, limit, offset)
	} else {
		rows, err = s.appPool.Query(ctx, `
			SELECT id, saved_query_id, sql, narrative_json, metrics, created_at, llm_model, llm_provider, connection_id
			FROM app.reports
			ORDER BY created_at DESC
			LIMIT $1 OFFSET $2
		`, limit, offset)
	}

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]*reports.Report, 0, limit)
	for rows.Next() {
		var report reports.Report
		var savedQueryID sql.NullString
		var narrativeJSON []byte
		var metricsJSON []byte
		var createdAt time.Time

		if err := rows.Scan(&report.ID, &savedQueryID, &report.SQL, &narrativeJSON, &metricsJSON, &createdAt, &report.LlmModel, &report.LlmProvider, &report.ConnectionID); err != nil {
			return nil, err
		}

		if savedQueryID.Valid {
			report.SavedQueryID = &savedQueryID.String
		}

		var narrative story.NarrativeContent
		if err := json.Unmarshal(narrativeJSON, &narrative); err == nil {
			report.Narrative = &reports.NarrativeContent{
				Headline:        narrative.Headline,
				Takeaways:       narrative.Takeaways,
				Drivers:         narrative.Drivers,
				Limitations:     narrative.Limitations,
				Recommendations: narrative.Recommendations,
			}
		}

		var calcMetrics metrics.Metrics
		if err := json.Unmarshal(metricsJSON, &calcMetrics); err == nil {
			report.Metrics = ConvertMetrics(&calcMetrics)
		}

		report.CreatedAt = createdAt.Format(time.RFC3339)
		items = append(items, &report)
	}

	if rows.Err() != nil {
		return nil, rows.Err()
	}

	return &reports.ReportList{
		Items:  items,
		Limit:  payload.Limit,
		Offset: payload.Offset,
	}, nil
}

// Similar finds reports semantically similar to the provided text.
func (s *ReportsService) Similar(ctx context.Context, payload *reports.SimilarPayload) (*reports.ReportSimilarResult, error) {
	if payload.Text == "" || s.embedder == nil || s.embeddingStore == nil {
		return &reports.ReportSimilarResult{Items: []*reports.SimilarReportItem{}}, nil
	}
	vec, err := s.embedder.Embed(ctx, payload.Text)
	if err != nil {
		return &reports.ReportSimilarResult{Items: []*reports.SimilarReportItem{}}, nil
	}
	connectionID := ""
	if payload.ConnectionID != nil {
		connectionID = *payload.ConnectionID
	}
	similar, err := s.embeddingStore.FindSimilarReports(ctx, vec, connectionID, int(payload.Limit))
	if err != nil {
		return &reports.ReportSimilarResult{Items: []*reports.SimilarReportItem{}}, nil
	}
	items := make([]*reports.SimilarReportItem, 0, len(similar))
	for _, r := range similar {
		items = append(items, &reports.SimilarReportItem{
			ID:           r.ReportID,
			Headline:     r.Headline,
			SQL:          r.SQL,
			ConnectionID: r.ConnectionID,
			CreatedAt:    r.CreatedAt,
			Similarity:   r.Similarity,
		})
	}
	return &reports.ReportSimilarResult{Items: items}, nil
}

// Rewrite rewrites an existing report narrative according to instruction.
func (s *ReportsService) Rewrite(ctx context.Context, payload *reports.RewritePayload) (*reports.NarrativeContent, error) {
	instruction := strings.TrimSpace(payload.Instruction)
	if instruction == "" {
		return nil, &reports.ValidationError{Name: "validation_error", Message: "instruction is required", Code: strPtr("VALIDATION_ERROR")}
	}
	row := s.appPool.QueryRow(ctx, `
		SELECT narrative_json, metrics
		FROM app.reports
		WHERE id = $1
	`, payload.ReportID)
	var narrativeJSON []byte
	var metricsJSON []byte
	if err := row.Scan(&narrativeJSON, &metricsJSON); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, &reports.NotFoundError{Name: "not_found", Message: "report not found", Code: strPtr("NOT_FOUND")}
		}
		return nil, err
	}
	prompt := llm.BuildNarrativeRewritePrompt(instruction, string(narrativeJSON), string(metricsJSON))
	raw, err := s.llmClient.Generate(ctx, prompt)
	if err != nil {
		return nil, &reports.LLMError{Name: "llm_error", Message: err.Error(), Code: strPtr("LLM_ERROR")}
	}
	rewritten, err := story.ParseNarrative(raw)
	if err != nil {
		return nil, &reports.LLMError{Name: "llm_error", Message: "failed to parse rewritten narrative", Code: strPtr("LLM_ERROR")}
	}
	return &reports.NarrativeContent{
		Headline:        rewritten.Headline,
		Takeaways:       rewritten.Takeaways,
		Drivers:         rewritten.Drivers,
		Limitations:     rewritten.Limitations,
		Recommendations: rewritten.Recommendations,
	}, nil
}

// CreateShare creates or refreshes a shareable read-only token for a report.
func (s *ReportsService) CreateShare(ctx context.Context, payload *reports.CreateSharePayload) (*reports.ReportShareLink, error) {
	var exists bool
	if err := s.appPool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM app.reports WHERE id = $1)`, payload.ReportID).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, &reports.NotFoundError{Name: "not_found", Message: "report not found", Code: strPtr("NOT_FOUND")}
	}
	token, err := newShareToken()
	if err != nil {
		return nil, err
	}
	expiresHours := 168
	if s.shareLinkDefaultHours > 0 {
		expiresHours = s.shareLinkDefaultHours
	}
	if payload.ExpiresInHours != nil && *payload.ExpiresInHours > 0 {
		expiresHours = int(*payload.ExpiresInHours)
	}
	if expiresHours > 8760 {
		return nil, &reports.ValidationError{Name: "validation_error", Message: "expires_in_hours must be at most 8760 (1 year)", Code: strPtr("VALIDATION_ERROR")}
	}
	var expiresAt sql.NullTime
	err = s.appPool.QueryRow(ctx, `
		INSERT INTO app.report_share_tokens (report_id, token, expires_at)
		VALUES ($1, $2, NOW() + ($3::text || ' hours')::interval)
		ON CONFLICT (report_id) DO UPDATE SET token = EXCLUDED.token, expires_at = EXCLUDED.expires_at, created_at = NOW()
		RETURNING expires_at
	`, payload.ReportID, token, expiresHours).Scan(&expiresAt)
	if err != nil {
		return nil, err
	}
	link := &reports.ReportShareLink{
		Token: token,
		URL:   "/shared/" + token,
	}
	if expiresAt.Valid {
		t := expiresAt.Time.UTC().Format(time.RFC3339)
		link.ExpiresAt = &t
	}
	return link, nil
}

// GetShared fetches a report through a valid share token.
func (s *ReportsService) GetShared(ctx context.Context, payload *reports.GetSharedPayload) (*reports.Report, error) {
	var reportID string
	err := s.appPool.QueryRow(ctx, `
		SELECT report_id
		FROM app.report_share_tokens
		WHERE token = $1
		  AND expires_at > NOW()
	`, payload.Token).Scan(&reportID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, &reports.NotFoundError{Name: "not_found", Message: "shared report link is invalid or expired", Code: strPtr("NOT_FOUND")}
		}
		return nil, err
	}
	return s.Get(ctx, &reports.GetPayload{ID: reportID})
}

func newShareToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// BuildPerfSuggestions returns performance suggestions from query result. Exported for testing.
func BuildPerfSuggestions(r *queryrunner.Result) []string {
	var suggestions []string
	if r.ExecutionTimeMs > 2000 {
		suggestions = append(suggestions, "Query took over 2s; consider adding filters or indexes.")
	}
	if r.RowCount >= 1000 {
		suggestions = append(suggestions, "Result set is large (limit applied); consider narrowing date range or dimensions.")
	}
	return suggestions
}

// ConvertMetrics converts app metrics to API type. Exported for testing.
func ConvertMetrics(m *metrics.Metrics) *reports.MetricsData {
	aggregates := make(map[string]*reports.AggregateData, len(m.Aggregates))
	for col, agg := range m.Aggregates {
		count := int32(agg.Count)
		ad := &reports.AggregateData{
			Sum:   agg.Sum,
			Avg:   agg.Avg,
			Min:   agg.Min,
			Max:   agg.Max,
			Count: &count,
		}
		if agg.StdDev != nil {
			ad.StdDev = agg.StdDev
		}
		aggregates[col] = ad
	}

	topCategories := make(map[string][]*reports.TopCategoryData, len(m.TopCategories))
	for col, cats := range m.TopCategories {
		categoryData := make([]*reports.TopCategoryData, len(cats))
		for i, cat := range cats {
			categoryData[i] = &reports.TopCategoryData{
				Category:   cat.Category,
				Value:      cat.Value,
				Percentage: cat.Percentage,
			}
		}
		topCategories[col] = categoryData
	}

	timeSeries := make(map[string]*reports.TimeSeriesData, len(m.TimeSeries))
	for col, ts := range m.TimeSeries {
		tsData := &reports.TimeSeriesData{
			CurrentPeriod: ts.CurrentPeriod,
			Trend:         ts.Trend,
		}
		if ts.PreviousPeriod != nil {
			tsData.PreviousPeriod = ts.PreviousPeriod
		}
		if ts.Change != nil {
			tsData.Change = ts.Change
		}
		if ts.ChangePercentage != nil {
			tsData.ChangePercentage = ts.ChangePercentage
		}
		if len(ts.Periods) > 0 {
			tsData.Periods = make([]*reports.PeriodPointData, len(ts.Periods))
			for i := range ts.Periods {
				tsData.Periods[i] = &reports.PeriodPointData{
					Label: ts.Periods[i].Label,
					Value: ts.Periods[i].Value,
				}
			}
		}
		if ts.MovingAverage != nil {
			tsData.MovingAverage = ts.MovingAverage
		}
		if len(ts.Anomalies) > 0 {
			tsData.Anomalies = make([]*reports.AnomalyPointData, len(ts.Anomalies))
			for i := range ts.Anomalies {
				tsData.Anomalies[i] = &reports.AnomalyPointData{
					PeriodLabel: ts.Anomalies[i].PeriodLabel,
					Value:       ts.Anomalies[i].Value,
					Reason:      ts.Anomalies[i].Reason,
					Explanation: strPtrIfNotEmpty(ts.Anomalies[i].Explanation),
				}
			}
		}
		if ts.TrendSummary != nil {
			pu := int32(ts.TrendSummary.PeriodsUsed)
			tsData.TrendSummary = &reports.TrendSummaryData{
				Direction:   ts.TrendSummary.Direction,
				Summary:     ts.TrendSummary.Summary,
				Slope:       &ts.TrendSummary.Slope,
				PeriodsUsed: &pu,
				Explanation: strPtrIfNotEmpty(ts.TrendSummary.Explanation),
			}
		}
		if ts.NextPeriodForecast != nil {
			tsData.NextPeriodForecast = ts.NextPeriodForecast
		}
		if ts.ForecastCILower != nil {
			tsData.ForecastCiLower = ts.ForecastCILower
		}
		if ts.ForecastCIUpper != nil {
			tsData.ForecastCiUpper = ts.ForecastCIUpper
		}
		if ts.PredictiveSummary != "" {
			tsData.PredictiveSummary = &ts.PredictiveSummary
		}
		if ts.ExponentialSmoothForecast != nil {
			tsData.ExponentialSmoothForecast = ts.ExponentialSmoothForecast
		}
		if ts.HoltForecast != nil {
			tsData.HoltForecast = ts.HoltForecast
		}
		if ts.SeasonalPeriod != 0 {
			sp := int32(ts.SeasonalPeriod)
			tsData.SeasonalPeriod = &sp
		}
		if ts.SeasonallyAdjustedForecast != nil {
			tsData.SeasonallyAdjustedForecast = ts.SeasonallyAdjustedForecast
		}
		timeSeries[col] = tsData
	}

	correlations := make([]*reports.CorrelationPairData, len(m.Correlations))
	for i := range m.Correlations {
		c := &m.Correlations[i]
		correlations[i] = &reports.CorrelationPairData{
			ColumnA:  c.ColumnA,
			ColumnB:  c.ColumnB,
			Pearson:  c.Pearson,
			Spearman: c.Spearman,
		}
	}

	dataQuality := make(map[string]*reports.ColumnQualityData, len(m.DataQuality))
	for col, q := range m.DataQuality {
		dataQuality[col] = &reports.ColumnQualityData{
			NullCount:     int32(q.NullCount),
			DistinctCount: int32(q.DistinctCount),
			TotalRows:     int32(q.TotalRows),
			NullPct:       q.NullPct,
		}
	}

	cohorts := make([]*reports.CohortMetricData, 0)
	if len(m.Cohorts) > 0 {
		cohorts = make([]*reports.CohortMetricData, len(m.Cohorts))
		for i := range m.Cohorts {
			co := &m.Cohorts[i]
			periods := make([]*reports.CohortPeriodPointData, len(co.Periods))
			for j := range co.Periods {
				periods[j] = &reports.CohortPeriodPointData{
					PeriodLabel: co.Periods[j].PeriodLabel,
					Value:       co.Periods[j].Value,
				}
			}
			cohorts[i] = &reports.CohortMetricData{
				CohortLabel:  co.CohortLabel,
				Periods:      periods,
				RetentionPct: co.RetentionPct,
			}
		}
	}
	out := &reports.MetricsData{
		Aggregates:      aggregates,
		TopCategories:   topCategories,
		TimeSeries:      timeSeries,
		Correlations:    correlations,
		Cohorts:         cohorts,
		DataQuality:     dataQuality,
		PerfSuggestions: m.PerfSuggestions,
	}
	if m.CurrentPeriodLabel != "" {
		out.PeriodCurrentLabel = &m.CurrentPeriodLabel
	}
	if m.PreviousPeriodLabel != "" {
		out.PeriodPreviousLabel = &m.PreviousPeriodLabel
	}
	return out
}
