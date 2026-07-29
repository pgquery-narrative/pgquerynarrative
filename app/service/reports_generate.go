package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pgquerynarrative/pgquerynarrative/api/gen/reports"
	"github.com/pgquerynarrative/pgquerynarrative/app/apilog"
	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/app/charts"
	"github.com/pgquerynarrative/pgquerynarrative/app/debuglog"
	"github.com/pgquerynarrative/pgquerynarrative/app/embedding"
	"github.com/pgquerynarrative/pgquerynarrative/app/format"
	"github.com/pgquerynarrative/pgquerynarrative/app/llm"
	"github.com/pgquerynarrative/pgquerynarrative/app/metrics"
	"github.com/pgquerynarrative/pgquerynarrative/app/queryrunner"
	"github.com/pgquerynarrative/pgquerynarrative/app/story"
)

func (s *ReportsService) generateReport(ctx context.Context, payload *reports.GenerateReportPayload, opts reportGenOpts) (*reports.Report, error) {
	ctx = withLLMCallBudget(ctx, s.maxLLMCallsPerReport)
	debuglog.Log("report generation started")
	connectionID, err := s.resolveConnectionID(payload.ConnectionID)
	if err != nil {
		return nil, &reports.ValidationError{Name: "validation_error", Message: "connection not found", Code: strPtr("CONNECTION_NOT_FOUND")}
	}
	if err := checkConnectionAccess(ctx, s.authz, connectionID, auth.ActionReport); err != nil {
		return nil, &reports.ValidationError{Name: "validation_error", Message: "connection access denied", Code: strPtr("CONNECTION_FORBIDDEN")}
	}
	runner, err := s.connectionResolver.runnerFor(payload.ConnectionID)
	if err != nil {
		return nil, &reports.ValidationError{Name: "validation_error", Message: "connection not found", Code: strPtr("CONNECTION_NOT_FOUND")}
	}
	queryResult, err := runner.Run(ctx, payload.SQL, 1000)
	if err != nil {
		kind, userMsg := ClassifyRunError(err)
		if kind == RunErrorTimeout {
			apilog.ValidationError("generate", "timeout_error", err.Error())
			return nil, &reports.ValidationError{Name: "timeout_error", Message: userMsg, Code: strPtr("TIMEOUT_ERROR")}
		}
		if kind == RunErrorTooLarge {
			apilog.ValidationError("generate", "query_result_too_large", err.Error())
			return nil, &reports.ValidationError{Name: "query_result_too_large", Message: userMsg, Code: strPtr("QUERY_RESULT_TOO_LARGE")}
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
		if vec, err := s.embedder.Embed(embedding.WithOperation(ctx, "embed_rag"), payload.SQL); err == nil {
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
			narrative = buildMetricsNarrative(queryResult.RowCount, calcMetrics)
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
	reportID, err := s.storeReport(ctx, payload, narrative, calcMetrics, queryResult, providerName, modelName, connectionID, opts.scheduleRunID)
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
	raw, err := s.invokeLLM(ctx, "trend_explanation", b.String(), false, false, "")
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
	raw, err := s.invokeLLM(ctx, "anomaly_explanation", b.String(), false, false, "")
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
	vec, err := s.embedder.Embed(embedding.WithOperation(ctx, "embed_report_store"), text)
	if err != nil {
		return
	}
	_ = s.embeddingStore.UpsertReport(ctx, reportID, vec, modelName)
}

func formatFloatPtr(v *float64) string {
	if v == nil {
		return "—"
	}
	return format.FloatWithCommas(*v)
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

// buildMetricsNarrative builds an evidence-based narrative from calculated metrics
// when the LLM narrative pass fails or is skipped. Prefer this over the stub fallback.
func buildMetricsNarrative(rowCount int, m *metrics.Metrics) *story.NarrativeContent {
	if m == nil {
		return buildFallbackNarrative(rowCount, nil)
	}
	n := &story.NarrativeContent{
		Headline:    "Query results — key metrics",
		Takeaways:   []string{"Query executed successfully and returned " + strconv.Itoa(rowCount) + " rows."},
		Limitations: []string{"Full LLM story could not be parsed; this narrative is grounded only in calculated metrics."},
	}

	// Prefer category breakdowns when present (most Ask demos).
	for measure, cats := range m.TopCategories {
		if len(cats) == 0 {
			continue
		}
		top := cats[0]
		n.Headline = top.Category + " leads on " + measure
		n.Takeaways = []string{
			fmt.Sprintf("%s accounts for %.1f%% of %s (value %s).", top.Category, top.Percentage, measure, format.FloatWithCommas(top.Value)),
			fmt.Sprintf("Query returned %d rows with a clear category ranking.", rowCount),
		}
		drivers := make([]string, 0, len(cats))
		for i, c := range cats {
			if i >= 5 {
				break
			}
			drivers = append(drivers, fmt.Sprintf("%s: %s (%.1f%%)", c.Category, format.FloatWithCommas(c.Value), c.Percentage))
		}
		n.Drivers = drivers
		break
	}

	if len(n.Drivers) == 0 {
		for col, agg := range m.Aggregates {
			n.Takeaways = append(n.Takeaways,
				fmt.Sprintf("%s — sum %s, avg %s, min %s, max %s across %d values.",
					col, formatFloatPtr(agg.Sum), formatFloatPtr(agg.Avg),
					formatFloatPtr(agg.Min), formatFloatPtr(agg.Max), agg.Count))
			break
		}
	}

	if len(m.PerfSuggestions) > 0 {
		n.Recommendations = append(n.Recommendations, m.PerfSuggestions...)
	} else {
		n.Recommendations = append(n.Recommendations, "Review the SQL and charts below; regenerate with a cloud LLM for a richer prose narrative if needed.")
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
	raw, err := s.invokeLLM(ctx, "chart_recommendation", prompt, false, false, "")
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

func (s *ReportsService) storeReport(ctx context.Context, payload *reports.GenerateReportPayload, narrative *story.NarrativeContent, calcMetrics *metrics.Metrics, queryResult *queryrunner.Result, providerName, modelName, connectionID, scheduleRunID string) (string, error) {
	narrativeJSON, _ := json.Marshal(narrative)
	metricsJSON, _ := json.Marshal(calcMetrics)
	statsJSON, _ := json.Marshal(map[string]interface{}{
		"execution_time_ms": queryResult.ExecutionTimeMs,
		"row_count":         queryResult.RowCount,
	})

	var reportID string
	p := auth.PrincipalFromContext(ctx)
	sqlAtRest, sealErr := sealProductSQL(s.dataEncKey, payload.SQL)
	if sealErr != nil {
		return "", sealErr
	}
	err := s.appPool.QueryRow(ctx, `
		INSERT INTO app.reports (
			saved_query_id, sql, narrative_md, narrative_json, metrics, stats,
			llm_model, llm_provider, success, connection_id, organization_id, created_by, schedule_run_id
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, NULLIF($13, '')::uuid)
		RETURNING id
	`, payload.SavedQueryID, sqlAtRest, narrative.Headline, narrativeJSON, metricsJSON, statsJSON,
		modelName, providerName, true, connectionID, p.OrgID, p.UserID, scheduleRunID).Scan(&reportID)
	if err != nil {
		// Two workers can race to generate the first report for the same schedule_run_id
		// (e.g. lease recovery overlapping the original worker). The partial unique index on
		// schedule_run_id turns the loser's insert into a conflict instead of a duplicate
		// report/report_id; reuse the winner's report rather than surfacing an error.
		if scheduleRunID != "" && isUniqueViolation(err) {
			if existingID, selErr := s.reportIDForScheduleRun(ctx, scheduleRunID); selErr == nil && existingID != "" {
				return existingID, nil
			}
		}
		return "", err
	}
	return reportID, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
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
