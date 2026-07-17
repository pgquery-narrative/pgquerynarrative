package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/api/gen/reports"
	"github.com/pgquerynarrative/pgquerynarrative/app/config"
	"github.com/pgquerynarrative/pgquerynarrative/app/db"
	"github.com/pgquerynarrative/pgquerynarrative/app/embedding"
	"github.com/pgquerynarrative/pgquerynarrative/app/llm"
	"github.com/pgquerynarrative/pgquerynarrative/app/metrics"
	"github.com/pgquerynarrative/pgquerynarrative/app/queryrunner"
	"github.com/pgquerynarrative/pgquerynarrative/app/story"
)

type ReportsService struct {
	readOnlyPool   *pgxpool.Pool
	appPool        db.DB
	runner         *queryrunner.Runner
	llmClient      llm.Client
	generator      *story.Generator
	metricsOpts    *metrics.Options
	embedder       embedding.Embedder
	embeddingStore *embedding.Store
	runners        map[string]*queryrunner.Runner
	connectionResolver
	shareLinkDefaultHours int
	shareLinksEnabled     bool
	llmAudit              *llm.AuditStore
	llmBudget             *llm.BudgetStore
	llmAllowCloud         bool
	maxLLMCallsPerReport  int
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

// SetLLMGovernance wires audit logging, budgets, and external-data policy for all LLM calls.
func (s *ReportsService) SetLLMGovernance(audit *llm.AuditStore, budget *llm.BudgetStore, allowCloud bool) {
	s.llmAudit = audit
	s.llmBudget = budget
	s.llmAllowCloud = allowCloud
	if s.generator != nil {
		s.generator.SetGovernance(audit, budget, allowCloud)
	}
}

// SetMaxLLMCallsPerReport caps auxiliary LLM calls (trend/chart explanations) per report generation.
func (s *ReportsService) SetMaxLLMCallsPerReport(max int) {
	if s != nil {
		s.maxLLMCallsPerReport = max
	}
}

func (s *ReportsService) invokeLLM(ctx context.Context, operation, prompt string, hasRows, hasRAG bool, sqlText string) (string, error) {
	if err := reserveLLMCall(ctx); err != nil {
		return "", err
	}
	gov := llm.GovernanceFromPrompt(s.PromptOptions(), s.llmAllowCloud, s.llmClient.Name(), hasRows, hasRAG, sqlText)
	return llm.InvokeWithBudget(ctx, s.llmClient, llm.InvokeOptions{Audit: s.llmAudit, Budget: s.llmBudget}, operation, gov, prompt)
}

// PromptOptions returns configured LLM prompt options for narrative generation.
func (s *ReportsService) PromptOptions() llm.PromptOptions {
	if s.generator != nil {
		return s.generator.PromptOptions()
	}
	return llm.DefaultPromptOptions()
}

// SetShareLinkDefaultHours sets the default TTL for share links when expires_in_hours is omitted.
func (s *ReportsService) SetShareLinkDefaultHours(hours int) {
	if hours > 0 {
		s.shareLinkDefaultHours = hours
	}
}

// SetShareLinksEnabled controls creation of public shared-report tokens.
func (s *ReportsService) SetShareLinksEnabled(enabled bool) {
	s.shareLinksEnabled = enabled
}

func NewReportsService(readOnlyPool *pgxpool.Pool, appPool db.DB, runner *queryrunner.Runner, llmClient llm.Client, metricsCfg config.MetricsConfig) *ReportsService {
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
func NewReportsServiceWithRAG(readOnlyPool *pgxpool.Pool, appPool db.DB, runner *queryrunner.Runner, llmClient llm.Client, metricsCfg config.MetricsConfig, embedder embedding.Embedder, embeddingStore *embedding.Store) *ReportsService {
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
func NewReportsServiceMultiConnection(appPool db.DB, runners map[string]*queryrunner.Runner, defaultConnectionID string, llmClient llm.Client, metricsCfg config.MetricsConfig, embedder embedding.Embedder, embeddingStore *embedding.Store) *ReportsService {
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

func (s *ReportsService) Get(ctx context.Context, payload *reports.GetPayload) (*reports.Report, error) {
	row := s.appPool.QueryRow(ctx, `
		SELECT id, saved_query_id, sql, narrative_json, metrics, created_at, llm_model, llm_provider, connection_id
		FROM app.reports
		WHERE id = $1 AND organization_id = $2
	`, payload.ID, orgID(ctx))

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
	oid := orgID(ctx)

	var rows pgx.Rows
	var err error

	if payload.SavedQueryID != nil && *payload.SavedQueryID != "" {
		if payload.ConnectionID != nil && *payload.ConnectionID != "" {
			rows, err = s.appPool.Query(ctx, `
			SELECT id, saved_query_id, sql, narrative_json, metrics, created_at, llm_model, llm_provider, connection_id
			FROM app.reports
			WHERE saved_query_id = $1 AND connection_id = $2 AND organization_id = $3
			ORDER BY created_at DESC
			LIMIT $4 OFFSET $5
		`, *payload.SavedQueryID, *payload.ConnectionID, oid, limit, offset)
		} else {
			rows, err = s.appPool.Query(ctx, `
			SELECT id, saved_query_id, sql, narrative_json, metrics, created_at, llm_model, llm_provider, connection_id
			FROM app.reports
			WHERE saved_query_id = $1 AND organization_id = $2
			ORDER BY created_at DESC
			LIMIT $3 OFFSET $4
		`, *payload.SavedQueryID, oid, limit, offset)
		}
	} else if payload.ConnectionID != nil && *payload.ConnectionID != "" {
		rows, err = s.appPool.Query(ctx, `
			SELECT id, saved_query_id, sql, narrative_json, metrics, created_at, llm_model, llm_provider, connection_id
			FROM app.reports
			WHERE connection_id = $1 AND organization_id = $2
			ORDER BY created_at DESC
			LIMIT $3 OFFSET $4
		`, *payload.ConnectionID, oid, limit, offset)
	} else {
		rows, err = s.appPool.Query(ctx, `
			SELECT id, saved_query_id, sql, narrative_json, metrics, created_at, llm_model, llm_provider, connection_id
			FROM app.reports
			WHERE organization_id = $1
			ORDER BY created_at DESC
			LIMIT $2 OFFSET $3
		`, oid, limit, offset)
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
		WHERE id = $1 AND organization_id = $2
	`, payload.ReportID, orgID(ctx))
	var narrativeJSON []byte
	var metricsJSON []byte
	if err := row.Scan(&narrativeJSON, &metricsJSON); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, &reports.NotFoundError{Name: "not_found", Message: "report not found", Code: strPtr("NOT_FOUND")}
		}
		return nil, err
	}
	prompt := llm.BuildNarrativeRewritePrompt(instruction, string(narrativeJSON), string(metricsJSON))
	raw, err := s.invokeLLM(ctx, "narrative_rewrite", prompt, false, false, instruction)
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
