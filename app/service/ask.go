package service

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/pgquerynarrative/pgquerynarrative/api/gen/reports"
	schemaapi "github.com/pgquerynarrative/pgquerynarrative/api/gen/schema"
	suggestions "github.com/pgquerynarrative/pgquerynarrative/api/gen/suggestions"
	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/app/catalog"
	"github.com/pgquerynarrative/pgquerynarrative/app/db"
	apperrors "github.com/pgquerynarrative/pgquerynarrative/app/errors"
	"github.com/pgquerynarrative/pgquerynarrative/app/llm"
	"github.com/pgquerynarrative/pgquerynarrative/app/queryrunner"
)

// AskService performs the full NL→SQL→report flow: load schema, generate SQL
// from a natural-language question via LLM, validate and run the query, then
// generate the narrative report.
type AskService struct {
	catalogLoader *catalog.Loader
	llmClient     llm.Client
	validator     *queryrunner.Validator
	reportsSvc    *ReportsService
	appPool       db.DB
	llmAudit      *llm.AuditStore
	llmBudget     *llm.BudgetStore
	llmAllowCloud bool
	connectionResolver
	authz ConnectionAuthorizer
}

// SetAuthorizer wires connection-level authorization (C5). Nil is permissive.
// Intended to be called only once, from narrative.NewClient, before the
// service is handed to any HTTP handler or background worker.
func (s *AskService) SetAuthorizer(authz ConnectionAuthorizer) {
	if s != nil {
		s.authz = authz
	}
}

// SetLLMGovernance wires audit logging, budgets, and external-data policy for Ask LLM calls.
// Intended to be called only once, from narrative.NewClient, before the
// service is handed to any HTTP handler.
func (s *AskService) SetLLMGovernance(audit *llm.AuditStore, budget *llm.BudgetStore, allowCloud bool) {
	s.llmAudit = audit
	s.llmBudget = budget
	s.llmAllowCloud = allowCloud
}

func (s *AskService) invokeLLM(ctx context.Context, operation, prompt, sourceText string) (string, error) {
	opts := llm.DefaultPromptOptions()
	if s.reportsSvc != nil {
		opts = s.reportsSvc.PromptOptions()
	}
	gov := llm.GovernanceFromPrompt(opts, s.llmAllowCloud, s.llmClient.Name(), false, false, sourceText)
	return llm.InvokeWithBudget(ctx, s.llmClient, llm.InvokeOptions{Audit: s.llmAudit, Budget: s.llmBudget}, operation, gov, prompt)
}

// NewAskService creates an AskService with the given dependencies.
func NewAskService(
	appPool db.DB,
	catalogLoader *catalog.Loader,
	llmClient llm.Client,
	validator *queryrunner.Validator,
	reportsSvc *ReportsService,
) *AskService {
	return &AskService{
		catalogLoader: catalogLoader,
		llmClient:     llmClient,
		validator:     validator,
		reportsSvc:    reportsSvc,
		appPool:       appPool,
		connectionResolver: newConnectionResolver("default", map[string]*queryrunner.Runner{
			"default": reportsSvc.runner,
		}, map[string]*catalog.Loader{
			"default": catalogLoader,
		}, nil),
	}
}

// NewAskServiceMultiConnection creates AskService with connection-aware schema loading and report generation.
func NewAskServiceMultiConnection(
	appPool db.DB,
	loaders map[string]*catalog.Loader,
	llmClient llm.Client,
	validator *queryrunner.Validator,
	reportsSvc *ReportsService,
	defaultConnectionID string,
) *AskService {
	var defaultLoader *catalog.Loader
	if l, ok := loaders[defaultConnectionID]; ok {
		defaultLoader = l
	} else {
		for _, l := range loaders {
			defaultLoader = l
			break
		}
	}
	return &AskService{
		catalogLoader:      defaultLoader,
		llmClient:          llmClient,
		validator:          validator,
		reportsSvc:         reportsSvc,
		appPool:            appPool,
		connectionResolver: newConnectionResolver(defaultConnectionID, reportsSvc.runners, loaders, nil),
	}
}

// authorizeConnectionAction checks connection-level authorization (C5) BEFORE
// resolving a loader/runner for connectionID (nil means "use the default connection").
func (s *AskService) authorizeConnectionAction(ctx context.Context, connectionID *string, action string) error {
	connID, err := s.connectionResolver.resolveConnectionID(connectionID)
	if err != nil {
		return nil // let the subsequent loaderFor/runnerFor call surface ErrConnectionNotFound
	}
	return checkConnectionAccess(ctx, s.authz, connID, action)
}

// askConnectionForbiddenError converts a connection-authorization denial into the
// suggestions API error type.
func askConnectionForbiddenError(err error) error {
	if err == nil {
		return nil
	}
	return &suggestions.ValidationError{Name: "validation_error", Message: "connection access denied", Code: strPtr("CONNECTION_FORBIDDEN")}
}

func askConnectionNotFoundError(err error) error {
	if err == nil {
		return nil
	}
	return &suggestions.ValidationError{Name: "validation_error", Message: "connection not found", Code: strPtr("CONNECTION_NOT_FOUND")}
}

func askValidationError(err error) error {
	if err == nil {
		return nil
	}
	return &suggestions.ValidationError{Name: "validation_error", Message: SanitizeAPIError(err, "Invalid request."), Code: strPtr("VALIDATION_ERROR")}
}

func askLLMError(code, fallback string, err error) error {
	if err == nil {
		return &suggestions.LLMError{Name: "llm_error", Message: fallback, Code: strPtr(code)}
	}
	return &suggestions.LLMError{Name: "llm_error", Message: SanitizeAPIError(err, fallback), Code: strPtr(code)}
}

// Ask implements the suggestions service Ask method: question → SQL → report.
func (s *AskService) Ask(ctx context.Context, payload *suggestions.AskPayload) (*suggestions.AskResult, error) {
	question := strings.TrimSpace(payload.Question)
	if question == "" {
		return nil, &suggestions.ValidationError{Name: "validation_error", Message: "question is required", Code: strPtr("VALIDATION_ERROR")}
	}
	connID, resolveErr := s.resolveConnectionID(payload.ConnectionID)
	if resolveErr != nil {
		return nil, askConnectionNotFoundError(resolveErr)
	}
	if err := checkConnectionAccess(ctx, s.authz, connID, auth.ActionAsk); err != nil {
		return nil, askConnectionForbiddenError(err)
	}

	schemaResult, err := func() (*schemaapi.SchemaResult, error) {
		loader, err := s.connectionResolver.loaderFor(payload.ConnectionID)
		if err != nil {
			return nil, err
		}
		return loader.Load(ctx)
	}()
	if err != nil {
		if errors.Is(err, apperrors.ErrConnectionNotFound) {
			return nil, askConnectionNotFoundError(err)
		}
		return nil, askLLMError("SCHEMA_ERROR", "failed to load schema", err)
	}
	schemaText := llm.FormatSchemaForNL2SQL(schemaResult)
	prompt := llm.BuildNL2SQLPrompt(question, schemaText)

	response, err := s.invokeLLM(ctx, "nl2sql", prompt, question)
	if err != nil {
		return nil, askLLMError("LLM_ERROR", "LLM request failed", err)
	}
	sql := llm.ParseSQLFromResponse(response)
	sql = strings.TrimSpace(sql)
	if sql == "" {
		return nil, &suggestions.LLMError{Name: "llm_error", Message: "LLM did not return any SQL", Code: strPtr("LLM_ERROR")}
	}

	runner, err := s.connectionResolver.runnerFor(nil)
	if err != nil {
		return nil, askConnectionNotFoundError(err)
	}
	if err := runner.ValidateQueryWithContext(ctx, sql); err != nil {
		return nil, askValidationError(err)
	}

	reportPayload := &reports.GenerateReportPayload{SQL: sql, ConnectionID: payload.ConnectionID}
	report, err := s.reportsSvc.GenerateForAsk(ctx, reportPayload)
	if err != nil {
		if ve, ok := err.(*reports.ValidationError); ok {
			return nil, &suggestions.ValidationError{Name: ve.Name, Message: SanitizeAPIError(errors.New(ve.Message), ve.Message), Code: ve.Code}
		}
		if le, ok := err.(*reports.LLMError); ok {
			return nil, &suggestions.LLMError{Name: le.Name, Message: SanitizeAPIError(errors.New(le.Message), "report generation failed"), Code: le.Code}
		}
		return nil, askLLMError("REPORT_ERROR", "report generation failed", err)
	}

	return &suggestions.AskResult{
		Question: question,
		SQL:      sql,
		Report:   reportToSuggestions(report),
	}, nil
}

// Chat runs conversational Ask with short in-session context memory.
func (s *AskService) Chat(ctx context.Context, payload *suggestions.ChatPayload) (*suggestions.ChatResult, error) {
	question := strings.TrimSpace(payload.Question)
	if question == "" {
		return nil, &suggestions.ValidationError{Name: "validation_error", Message: "question is required", Code: strPtr("VALIDATION_ERROR")}
	}
	connID, resolveErr := s.resolveConnectionID(payload.ConnectionID)
	if resolveErr != nil {
		return nil, askConnectionNotFoundError(resolveErr)
	}
	if err := checkConnectionAccess(ctx, s.authz, connID, auth.ActionAsk); err != nil {
		return nil, askConnectionForbiddenError(err)
	}
	sessionID, historyCtx, err := s.ensureSessionAndHistory(ctx, payload.SessionID, payload.ConnectionID)
	if err != nil {
		return nil, askLLMError("SESSION_ERROR", "failed to prepare chat session", err)
	}
	schemaResult, err := func() (*schemaapi.SchemaResult, error) {
		loader, err := s.connectionResolver.loaderFor(payload.ConnectionID)
		if err != nil {
			return nil, err
		}
		return loader.Load(ctx)
	}()
	if err != nil {
		if errors.Is(err, apperrors.ErrConnectionNotFound) {
			return nil, askConnectionNotFoundError(err)
		}
		return nil, askLLMError("SCHEMA_ERROR", "failed to load schema", err)
	}
	schemaText := llm.FormatSchemaForNL2SQL(schemaResult)
	prompt := llm.BuildNL2SQLPrompt(question, schemaText)
	if historyCtx != "" {
		prompt += "\n\nConversation context:\n" + historyCtx + "\nUse this context to refine the next SQL."
	}
	response, err := s.invokeLLM(ctx, "nl2sql", prompt, question)
	if err != nil {
		return nil, askLLMError("LLM_ERROR", "LLM request failed", err)
	}
	sqlText := strings.TrimSpace(llm.ParseSQLFromResponse(response))
	if sqlText == "" {
		return nil, &suggestions.LLMError{Name: "llm_error", Message: "LLM did not return any SQL", Code: strPtr("LLM_ERROR")}
	}
	runner, err := s.connectionResolver.runnerFor(nil)
	if err != nil {
		return nil, askConnectionNotFoundError(err)
	}
	if err := runner.ValidateQueryWithContext(ctx, sqlText); err != nil {
		return nil, askValidationError(err)
	}
	report, err := s.reportsSvc.GenerateForAsk(ctx, &reports.GenerateReportPayload{SQL: sqlText, ConnectionID: payload.ConnectionID})
	if err != nil {
		if ve, ok := err.(*reports.ValidationError); ok {
			return nil, &suggestions.ValidationError{Name: ve.Name, Message: SanitizeAPIError(errors.New(ve.Message), ve.Message), Code: ve.Code}
		}
		if le, ok := err.(*reports.LLMError); ok {
			return nil, &suggestions.LLMError{Name: le.Name, Message: SanitizeAPIError(errors.New(le.Message), "report generation failed"), Code: le.Code}
		}
		return nil, askLLMError("REPORT_ERROR", "report generation failed", err)
	}
	if err := s.appendChatMessage(ctx, sessionID, question, sqlText, report.ID); err != nil {
		return nil, askLLMError("SESSION_ERROR", "failed to persist chat message", err)
	}
	history, err := s.loadChatHistory(ctx, sessionID, 8)
	if err != nil {
		return nil, askLLMError("SESSION_ERROR", "failed to load chat history", err)
	}
	followUps := s.buildFollowUps(ctx, history, question)
	return &suggestions.ChatResult{
		SessionID: sessionID,
		Question:  question,
		SQL:       sqlText,
		Report:    reportToSuggestions(report),
		History:   history,
		FollowUps: followUps,
	}, nil
}

func (s *AskService) ensureSessionAndHistory(ctx context.Context, sessionID *string, connectionID *string) (string, string, error) {
	if s.appPool == nil {
		return "", "", sql.ErrConnDone
	}
	if sessionID != nil && strings.TrimSpace(*sessionID) != "" {
		sid := strings.TrimSpace(*sessionID)
		if err := s.assertSessionOrg(ctx, sid); err != nil {
			return "", "", err
		}
		history, err := s.loadChatHistory(ctx, sid, 6)
		if err != nil {
			return "", "", err
		}
		return strings.TrimSpace(*sessionID), summarizeChatHistory(history), nil
	}
	id, err := s.resolveConnectionID(connectionID)
	if err != nil {
		return "", "", err
	}
	sid, err := s.createChatSession(ctx, id)
	if err != nil {
		return "", "", err
	}
	return sid, "", nil
}

func (s *AskService) createChatSession(ctx context.Context, connectionID string) (string, error) {
	var id string
	err := s.appPool.QueryRow(ctx, `
		INSERT INTO app.ask_sessions (connection_id, organization_id)
		VALUES ($1, $2)
		RETURNING id
	`, connectionID, orgID(ctx)).Scan(&id)
	return id, err
}

func (s *AskService) assertSessionOrg(ctx context.Context, sessionID string) error {
	var exists bool
	err := s.appPool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM app.ask_sessions WHERE id = $1 AND organization_id = $2)
	`, sessionID, orgID(ctx)).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return orgNotFound()
	}
	return nil
}

func (s *AskService) appendChatMessage(ctx context.Context, sessionID, question, sqlText, reportID string) error {
	_, err := s.appPool.Exec(ctx, `
		INSERT INTO app.ask_messages (session_id, question, sql, report_id)
		VALUES ($1, $2, $3, NULLIF($4, '')::uuid)
	`, sessionID, question, sqlText, reportID)
	if err != nil {
		return err
	}
	_, err = s.appPool.Exec(ctx, `
		UPDATE app.ask_sessions SET updated_at = NOW()
		WHERE id = $1 AND organization_id = $2
	`, sessionID, orgID(ctx))
	return err
}

func (s *AskService) loadChatHistory(ctx context.Context, sessionID string, limit int) ([]*suggestions.ChatTurn, error) {
	rows, err := s.appPool.Query(ctx, `
		SELECT question, sql, created_at
		FROM app.ask_messages
		WHERE session_id = $1
		ORDER BY created_at DESC
		LIMIT $2
	`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tmp := make([]*suggestions.ChatTurn, 0, limit)
	for rows.Next() {
		var question, sqlText string
		var createdAt time.Time
		if err := rows.Scan(&question, &sqlText, &createdAt); err != nil {
			return nil, err
		}
		tmp = append(tmp, &suggestions.ChatTurn{
			Question:  question,
			SQL:       sqlText,
			CreatedAt: createdAt.Format(time.RFC3339),
		})
	}
	// reverse to chronological
	history := make([]*suggestions.ChatTurn, 0, len(tmp))
	for i := len(tmp) - 1; i >= 0; i-- {
		history = append(history, tmp[i])
	}
	return history, rows.Err()
}

func summarizeChatHistory(history []*suggestions.ChatTurn) string {
	if len(history) == 0 {
		return ""
	}
	var b strings.Builder
	for _, h := range history {
		if h == nil {
			continue
		}
		b.WriteString("- Q: ")
		b.WriteString(h.Question)
		b.WriteString("\n  SQL: ")
		b.WriteString(h.SQL)
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

func (s *AskService) buildFollowUps(ctx context.Context, history []*suggestions.ChatTurn, latestQuestion string) []string {
	if len(history) == 0 || s.llmClient == nil {
		return defaultFollowUps(latestQuestion)
	}
	// Third LLM round-trip per Ask; skip for Ollama so Ask returns in reasonable time.
	if s.llmClient.Name() == "ollama" {
		return defaultFollowUps(latestQuestion)
	}
	var b strings.Builder
	b.WriteString("Suggest 3 short follow-up analytics questions for this conversation.\n")
	b.WriteString("Return one question per line, no numbering.\n\n")
	b.WriteString("Conversation:\n")
	b.WriteString(summarizeChatHistory(history))
	raw, err := s.invokeLLM(ctx, "follow_up_questions", b.String(), latestQuestion)
	if err != nil {
		return defaultFollowUps(latestQuestion)
	}
	lines := strings.Split(raw, "\n")
	out := make([]string, 0, 3)
	for _, l := range lines {
		t := strings.TrimSpace(strings.TrimPrefix(l, "- "))
		t = strings.TrimLeft(t, "0123456789.) ")
		if t == "" {
			continue
		}
		if !strings.HasSuffix(t, "?") {
			t += "?"
		}
		out = append(out, t)
		if len(out) == 3 {
			break
		}
	}
	if len(out) == 0 {
		return defaultFollowUps(latestQuestion)
	}
	return out
}

func defaultFollowUps(_ string) []string {
	return []string{
		"Can you break this down by region?",
		"What changed compared to the previous period?",
		"Show only the top 5 contributors.",
	}
}

// Explain implements the suggestions service Explain method: SQL → plain-English explanation.
func (s *AskService) Explain(ctx context.Context, payload *suggestions.ExplainPayload) (*suggestions.ExplainResult, error) {
	sql := strings.TrimSpace(payload.SQL)
	if sql == "" {
		return nil, &suggestions.ValidationError{Name: "validation_error", Message: "sql is required", Code: strPtr("VALIDATION_ERROR")}
	}
	sql = strings.TrimSuffix(sql, ";")
	sql = strings.TrimSpace(sql)
	runner, err := s.connectionResolver.runnerFor(nil)
	if err != nil {
		return nil, askConnectionNotFoundError(err)
	}
	if err := runner.ValidateQueryWithContext(ctx, sql); err != nil {
		return nil, askValidationError(err)
	}
	prompt := llm.BuildExplainPrompt(sql)
	response, err := s.invokeLLM(ctx, "nl2sql", prompt, sql)
	if err != nil {
		return nil, askLLMError("LLM_ERROR", "LLM request failed", err)
	}
	explanation := strings.TrimSpace(response)
	if explanation == "" {
		explanation = "No explanation returned."
	}
	return &suggestions.ExplainResult{SQL: sql, Explanation: explanation}, nil
}

// Questions suggests schema-driven natural-language prompts users can ask.
func (s *AskService) Questions(ctx context.Context, payload *suggestions.QuestionsPayload) (*suggestions.SuggestedQuestionsResult, error) {
	if err := s.authorizeConnectionAction(ctx, payload.ConnectionID, auth.ActionAsk); err != nil {
		return nil, askConnectionForbiddenError(err)
	}
	loader, err := s.connectionResolver.loaderFor(payload.ConnectionID)
	if err != nil {
		if errors.Is(err, apperrors.ErrConnectionNotFound) {
			return nil, askConnectionNotFoundError(err)
		}
		return &suggestions.SuggestedQuestionsResult{Questions: defaultQuestions()}, nil
	}
	schemaResult, err := loader.Load(ctx)
	if err != nil {
		return &suggestions.SuggestedQuestionsResult{Questions: defaultQuestions()}, nil
	}
	schemaText := llm.FormatSchemaForNL2SQL(schemaResult)
	limit := int(payload.Limit)
	if limit <= 0 {
		limit = 8
	}
	prompt := buildQuestionDiscoveryPrompt(schemaText, limit)
	raw, err := s.invokeLLM(ctx, "question_discovery", prompt, "")
	if err != nil {
		qs := defaultQuestions()
		if len(qs) > limit {
			qs = qs[:limit]
		}
		return &suggestions.SuggestedQuestionsResult{Questions: qs}, nil
	}
	parsed := parseSuggestedQuestions(raw, limit)
	if len(parsed) == 0 {
		parsed = defaultQuestions()
		if len(parsed) > limit {
			parsed = parsed[:limit]
		}
	}
	return &suggestions.SuggestedQuestionsResult{Questions: parsed}, nil
}

func buildQuestionDiscoveryPrompt(schemaText string, limit int) string {
	var b strings.Builder
	b.WriteString("You suggest practical business analytics questions based on a SQL schema.\n")
	b.WriteString("Return exactly ")
	b.WriteString(strconv.Itoa(limit))
	b.WriteString(" short natural-language questions.\n")
	b.WriteString("Constraints: no SQL, no numbering, one question per line, under 110 chars each.\n")
	b.WriteString("Focus on trends, top items, regional breakdowns, category comparisons, and recent period changes.\n\n")
	b.WriteString("Schema:\n")
	b.WriteString(schemaText)
	return b.String()
}

func parseSuggestedQuestions(raw string, limit int) []string {
	lines := strings.Split(raw, "\n")
	out := make([]string, 0, limit)
	seen := map[string]bool{}
	for _, line := range lines {
		q := strings.TrimSpace(line)
		q = strings.TrimPrefix(q, "- ")
		q = strings.TrimLeft(q, "0123456789.) ")
		q = strings.TrimSpace(q)
		if q == "" {
			continue
		}
		if !strings.HasSuffix(q, "?") {
			q += "?"
		}
		key := strings.ToLower(q)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, q)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func defaultQuestions() []string {
	return []string{
		"What were the top products by revenue in the latest period?",
		"How has total sales trended over time?",
		"Which regions are growing fastest month over month?",
		"Which categories contribute most to total revenue?",
		"Where do we see the largest decline compared to the previous period?",
		"Which products have high quantity but low revenue?",
		"What is the average order value trend by month?",
		"Which segment should we prioritize based on recent performance?",
	}
}

// reportToSuggestions copies a reports.Report into a suggestions.Report (same design, different packages).
func reportToSuggestions(r *reports.Report) *suggestions.Report {
	if r == nil {
		return nil
	}
	out := &suggestions.Report{
		ID:           r.ID,
		SavedQueryID: r.SavedQueryID,
		SQL:          r.SQL,
		CreatedAt:    r.CreatedAt,
		LlmModel:     r.LlmModel,
		LlmProvider:  r.LlmProvider,
	}
	if r.Narrative != nil {
		out.Narrative = &suggestions.NarrativeContent{
			Headline:        r.Narrative.Headline,
			Takeaways:       append([]string(nil), r.Narrative.Takeaways...),
			Drivers:         append([]string(nil), r.Narrative.Drivers...),
			Limitations:     append([]string(nil), r.Narrative.Limitations...),
			Recommendations: append([]string(nil), r.Narrative.Recommendations...),
		}
	}
	if r.Metrics != nil {
		out.Metrics = copyMetricsToSuggestions(r.Metrics)
	}
	if len(r.ChartSuggestions) > 0 {
		out.ChartSuggestions = make([]*suggestions.ChartSuggestion, len(r.ChartSuggestions))
		for i, c := range r.ChartSuggestions {
			if c != nil {
				out.ChartSuggestions[i] = &suggestions.ChartSuggestion{ChartType: c.ChartType, Label: c.Label, Reason: c.Reason}
			}
		}
	}
	return out
}

func copyMetricsToSuggestions(m *reports.MetricsData) *suggestions.MetricsData {
	if m == nil {
		return nil
	}
	out := &suggestions.MetricsData{
		PeriodCurrentLabel:  m.PeriodCurrentLabel,
		PeriodPreviousLabel: m.PeriodPreviousLabel,
		PerfSuggestions:     append([]string(nil), m.PerfSuggestions...),
	}
	if len(m.Correlations) > 0 {
		out.Correlations = make([]*suggestions.CorrelationPairData, len(m.Correlations))
		for i, c := range m.Correlations {
			if c != nil {
				out.Correlations[i] = &suggestions.CorrelationPairData{
					ColumnA:  c.ColumnA,
					ColumnB:  c.ColumnB,
					Pearson:  c.Pearson,
					Spearman: c.Spearman,
				}
			}
		}
	}
	if m.Aggregates != nil {
		out.Aggregates = make(map[string]*suggestions.AggregateData)
		for k, v := range m.Aggregates {
			if v != nil {
				out.Aggregates[k] = &suggestions.AggregateData{Sum: v.Sum, Avg: v.Avg, Min: v.Min, Max: v.Max, Count: v.Count, StdDev: v.StdDev}
			}
		}
	}
	if m.TopCategories != nil {
		out.TopCategories = make(map[string][]*suggestions.TopCategoryData)
		for k, arr := range m.TopCategories {
			for _, t := range arr {
				if t != nil {
					out.TopCategories[k] = append(out.TopCategories[k], &suggestions.TopCategoryData{Category: t.Category, Value: t.Value, Percentage: t.Percentage})
				}
			}
		}
	}
	if m.TimeSeries != nil {
		out.TimeSeries = make(map[string]*suggestions.TimeSeriesData)
		for k, v := range m.TimeSeries {
			if v != nil {
				ts := &suggestions.TimeSeriesData{
					CurrentPeriod:              v.CurrentPeriod,
					PreviousPeriod:             v.PreviousPeriod,
					Change:                     v.Change,
					ChangePercentage:           v.ChangePercentage,
					Trend:                      v.Trend,
					MovingAverage:              v.MovingAverage,
					NextPeriodForecast:         v.NextPeriodForecast,
					ForecastCiLower:            v.ForecastCiLower,
					ForecastCiUpper:            v.ForecastCiUpper,
					PredictiveSummary:          v.PredictiveSummary,
					ExponentialSmoothForecast:  v.ExponentialSmoothForecast,
					HoltForecast:               v.HoltForecast,
					SeasonalPeriod:             v.SeasonalPeriod,
					SeasonallyAdjustedForecast: v.SeasonallyAdjustedForecast,
				}
				for _, p := range v.Periods {
					if p != nil {
						ts.Periods = append(ts.Periods, &suggestions.PeriodPointData{Label: p.Label, Value: p.Value})
					}
				}
				for _, a := range v.Anomalies {
					if a != nil {
						ts.Anomalies = append(ts.Anomalies, &suggestions.AnomalyPointData{PeriodLabel: a.PeriodLabel, Value: a.Value, Reason: a.Reason, Explanation: a.Explanation})
					}
				}
				if v.TrendSummary != nil {
					ts.TrendSummary = &suggestions.TrendSummaryData{Direction: v.TrendSummary.Direction, Slope: v.TrendSummary.Slope, PeriodsUsed: v.TrendSummary.PeriodsUsed, Summary: v.TrendSummary.Summary, Explanation: v.TrendSummary.Explanation}
				}
				out.TimeSeries[k] = ts
			}
		}
	}
	if m.DataQuality != nil {
		out.DataQuality = make(map[string]*suggestions.ColumnQualityData)
		for k, v := range m.DataQuality {
			if v != nil {
				out.DataQuality[k] = &suggestions.ColumnQualityData{NullCount: v.NullCount, DistinctCount: v.DistinctCount, TotalRows: v.TotalRows, NullPct: v.NullPct}
			}
		}
	}
	if len(m.Cohorts) > 0 {
		out.Cohorts = make([]*suggestions.CohortMetricData, len(m.Cohorts))
		for i, co := range m.Cohorts {
			if co == nil {
				continue
			}
			periods := make([]*suggestions.CohortPeriodPointData, len(co.Periods))
			for j, p := range co.Periods {
				if p != nil {
					periods[j] = &suggestions.CohortPeriodPointData{PeriodLabel: p.PeriodLabel, Value: p.Value}
				}
			}
			out.Cohorts[i] = &suggestions.CohortMetricData{
				CohortLabel:  co.CohortLabel,
				Periods:      periods,
				RetentionPct: co.RetentionPct,
			}
		}
	}
	return out
}

// SuggestionsServiceWrapper implements suggestions.Service by delegating
// Queries and Similar to Suggester and Ask to AskService.
type SuggestionsServiceWrapper struct {
	Suggester suggestionsSuggester
	AskSvc    *AskService
}

// suggestionsSuggester is the interface used by the wrapper.
type suggestionsSuggester interface {
	Queries(context.Context, *suggestions.QueriesPayload) (*suggestions.SuggestedQueriesResult, error)
	Similar(context.Context, *suggestions.SimilarPayload) (*suggestions.SuggestedQueriesResult, error)
}

// Queries delegates to the suggester.
func (w *SuggestionsServiceWrapper) Queries(ctx context.Context, p *suggestions.QueriesPayload) (*suggestions.SuggestedQueriesResult, error) {
	return w.Suggester.Queries(ctx, p)
}

// Similar delegates to the suggester.
func (w *SuggestionsServiceWrapper) Similar(ctx context.Context, p *suggestions.SimilarPayload) (*suggestions.SuggestedQueriesResult, error) {
	return w.Suggester.Similar(ctx, p)
}

// Ask delegates to the AskService.
func (w *SuggestionsServiceWrapper) Ask(ctx context.Context, p *suggestions.AskPayload) (*suggestions.AskResult, error) {
	return w.AskSvc.Ask(ctx, p)
}

// Explain delegates to the AskService.
func (w *SuggestionsServiceWrapper) Explain(ctx context.Context, p *suggestions.ExplainPayload) (*suggestions.ExplainResult, error) {
	return w.AskSvc.Explain(ctx, p)
}

// Questions delegates schema-driven discovery prompts to AskService.
func (w *SuggestionsServiceWrapper) Questions(ctx context.Context, p *suggestions.QuestionsPayload) (*suggestions.SuggestedQuestionsResult, error) {
	return w.AskSvc.Questions(ctx, p)
}

// Chat delegates conversational ask to AskService.
func (w *SuggestionsServiceWrapper) Chat(ctx context.Context, p *suggestions.ChatPayload) (*suggestions.ChatResult, error) {
	return w.AskSvc.Chat(ctx, p)
}
