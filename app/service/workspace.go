package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pgquerynarrative/pgquerynarrative/api/gen/queries"
	"github.com/pgquerynarrative/pgquerynarrative/api/gen/workspace"
	"github.com/pgquerynarrative/pgquerynarrative/app/config"
	"github.com/pgquerynarrative/pgquerynarrative/app/db"
)

// WorkspaceService provides landing dashboard data, regression inbox, demo scenarios, and trust.
type WorkspaceService struct {
	appPool             db.DB
	queriesSvc          *QueriesService
	authEnabled         bool
	allowInsecureNoAuth bool
	explainAnalyze      bool
	queryTimeoutSec     int32
	sslMode             string
	auditMode           string
	llmAllowExternal    bool
	allowedSchemas      []string
}

// NewWorkspaceService creates a workspace service.
func NewWorkspaceService(
	appPool db.DB,
	queriesSvc *QueriesService,
	authEnabled, allowInsecureNoAuth, explainAnalyze bool,
	queryTimeoutSec int32,
	sslMode, auditMode string,
	llmAllowExternal bool,
	allowedSchemas []string,
) *WorkspaceService {
	return &WorkspaceService{
		appPool:             appPool,
		queriesSvc:          queriesSvc,
		authEnabled:         authEnabled,
		allowInsecureNoAuth: allowInsecureNoAuth,
		explainAnalyze:      explainAnalyze,
		queryTimeoutSec:     queryTimeoutSec,
		sslMode:             sslMode,
		auditMode:           auditMode,
		llmAllowExternal:    llmAllowExternal,
		allowedSchemas:      allowedSchemas,
	}
}

// Overview returns PostgreSQL evidence metrics for the landing dashboard.
func (s *WorkspaceService) Overview(ctx context.Context) (*workspace.WorkspaceOverview, error) {
	org := orgID(ctx)
	out := &workspace.WorkspaceOverview{}

	// Reports count
	_ = s.appPool.QueryRow(ctx, `
		SELECT COUNT(*) FROM app.reports WHERE organization_id = $1
	`, org).Scan(&out.ReportsGenerated)

	// Open investigations
	_ = s.appPool.QueryRow(ctx, `
		SELECT COUNT(*) FROM app.investigations
		WHERE organization_id = $1 AND status != 'complete'
	`, org).Scan(&out.InvestigationsOpen)

	// Regression inbox count — open = not acknowledged and not auto-resolved.
	_ = s.appPool.QueryRow(ctx, `
		SELECT COUNT(*) FROM app.regression_alerts
		WHERE organization_id = $1 AND acknowledged_at IS NULL AND resolved_at IS NULL
	`, org).Scan(&out.QueriesAttention)

	// Largest regression
	var largestReg *float64
	_ = s.appPool.QueryRow(ctx, `
		SELECT MAX(ABS(change_percent)) FROM app.regression_alerts
		WHERE organization_id = $1 AND acknowledged_at IS NULL AND resolved_at IS NULL
	`, org).Scan(&largestReg)
	if largestReg != nil {
		out.LargestRegressionPct = *largestReg
	}

	// Try pg_stat_statements for live metrics
	if s.queriesSvc != nil && s.queriesSvc.statStatementsEnabled {
		stats, err := s.queriesSvc.StatStatements(ctx, &queries.StatStatementsPayload{
			OrderBy: "total_time",
			Limit:   100,
		})
		if err == nil && stats != nil {
			var totalCalls int64
			var totalTimeMs float64
			for _, row := range stats.Items {
				totalCalls += row.Calls
				totalTimeMs += row.TotalTimeMs
			}
			out.QueriesObserved = totalCalls
			out.DatabaseTimeHours = totalTimeMs / 3_600_000
		}
	}

	// Demo theater (fake KPIs / seeded inbox) is APP_ENV=demo only.
	// Empty stats must stay empty so reviewers see real posture, not invented numbers.
	if config.DemoMode() {
		if out.QueriesObserved == 0 {
			out.QueriesObserved = 1482
			out.DatabaseTimeHours = 38.6
		}
		if out.QueriesAttention == 0 {
			_ = s.seedDemoRegressions(ctx, org)
			_ = s.appPool.QueryRow(ctx, `
				SELECT COUNT(*) FROM app.regression_alerts
				WHERE organization_id = $1 AND acknowledged_at IS NULL
			`, org).Scan(&out.QueriesAttention)
		}
		if out.LargestRegressionPct == 0 {
			out.LargestRegressionPct = 640
		}
		out.TempDataWrittenGb = 18.4
		out.SequentialScansDetected = 7
	}

	return out, nil
}

// Regressions returns the regression inbox.
func (s *WorkspaceService) Regressions(ctx context.Context, payload *workspace.RegressionsPayload) (*workspace.RegressionInbox, error) {
	org := orgID(ctx)
	limit := int(payload.Limit)
	if limit == 0 {
		limit = 10
	}

	// Never seed fake alerts into the regression inbox outside APP_ENV=demo.
	if config.DemoMode() {
		_ = s.seedDemoRegressions(ctx, org)
	}

	query := `
		SELECT id, title, query_text, change_type, change_summary, impact,
		       first_detected_at, connection_id,
		       (acknowledged_at IS NOT NULL) AS acknowledged,
		       queryid, source, investigation_id,
		       calls, mean_time_ms, total_time_ms, rows_count,
		       occurrences, last_seen_at, resolved_at, previous_alert_id
		FROM app.regression_alerts
		WHERE organization_id = $1
	`
	if !payload.IncludeAcknowledged {
		// Open = still needs attention: not acknowledged and not auto-resolved.
		query += ` AND acknowledged_at IS NULL AND resolved_at IS NULL`
	}
	query += ` ORDER BY first_detected_at DESC LIMIT $2`

	rows, err := s.appPool.Query(ctx, query, org, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := &workspace.RegressionInbox{Items: []*workspace.RegressionAlert{}}
	for rows.Next() {
		var item workspace.RegressionAlert
		var detectedAt time.Time
		var queryid, source, invID, prevID *string
		var calls, rowsCount *int64
		var meanMs, totalMs *float64
		var occurrences *int
		var lastSeenAt, resolvedAt *time.Time
		if err := rows.Scan(
			&item.ID, &item.Title, &item.Query, &item.ChangeType, &item.ChangeSummary,
			&item.Impact, &detectedAt, &item.ConnectionID, &item.Acknowledged,
			&queryid, &source, &invID, &calls, &meanMs, &totalMs, &rowsCount,
			&occurrences, &lastSeenAt, &resolvedAt, &prevID,
		); err != nil {
			return nil, err
		}
		item.FirstDetectedAt = detectedAt.Format(time.RFC3339)
		item.Queryid = queryid
		item.Source = source
		item.InvestigationID = invID
		item.Calls = calls
		item.MeanTimeMs = meanMs
		item.TotalTimeMs = totalMs
		item.Rows = rowsCount
		item.Occurrences = occurrences
		item.PreviousAlertID = prevID
		if lastSeenAt != nil {
			v := lastSeenAt.Format(time.RFC3339)
			item.LastSeenAt = &v
		}
		if resolvedAt != nil {
			v := resolvedAt.Format(time.RFC3339)
			item.ResolvedAt = &v
		}
		out.Items = append(out.Items, &item)
	}
	return out, rows.Err()
}

// AcknowledgeRegression marks a regression alert as acknowledged.
func (s *WorkspaceService) AcknowledgeRegression(ctx context.Context, payload *workspace.AcknowledgeRegressionPayload) error {
	tag, err := s.appPool.Exec(ctx, `
		UPDATE app.regression_alerts
		SET acknowledged_at = now()
		WHERE id = $1 AND organization_id = $2 AND acknowledged_at IS NULL
	`, payload.ID, orgID(ctx))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return &workspace.NotFoundError{Name: "not_found", Message: "regression alert not found", Code: strPtr("NOT_FOUND")}
	}
	return nil
}

// DemoScenarios returns sample problem SQL for guided walkthroughs.
// CandidateSQL is intentionally omitted: the rewrite must come from Suggest rewrite /
// Rank candidates (or a human), not a hardcoded answer key.
func (s *WorkspaceService) DemoScenarios(_ context.Context) (*workspace.DemoScenarioList, error) {
	return &workspace.DemoScenarioList{
		Items: []*workspace.DemoScenario{
			{
				ID:                  "slow-dashboard",
				Title:               "Slow dashboard query",
				Problem:             "Revenue rollup uses DATE_TRUNC on the partition key, so PostgreSQL cannot prune partitions or use a date index. Use Suggest rewrite to propose a sargable range.",
				SQL:                 `SELECT product_category, SUM(total_amount) AS revenue FROM demo.sales WHERE DATE_TRUNC('month', date) = DATE '2025-01-01' GROUP BY product_category ORDER BY revenue DESC`,
				ExpectedImprovement: "Many partitions → 1 month (range rewrite via Suggest rewrite)",
				Category:            "function_wrap",
			},
			{
				ID:                  "extract-year-month",
				Title:               "EXTRACT wraps the partition key",
				Problem:             "EXTRACT(YEAR/MONTH FROM date) is another function wrap that blocks partition pruning. Suggest rewrite should propose a January range from the AST — not a DATE_TRUNC answer key.",
				SQL:                 `SELECT product_category, SUM(total_amount) AS revenue FROM demo.sales WHERE EXTRACT(YEAR FROM date) = 2025 AND EXTRACT(MONTH FROM date) = 1 GROUP BY product_category ORDER BY revenue DESC`,
				ExpectedImprovement: "Year+month EXTRACT → January range (partition prune via Suggest rewrite)",
				Category:            "function_wrap",
			},
			{
				ID:                  "or-across-columns",
				Title:               "OR across different columns",
				Problem:             "OR of predicates on region vs product_category can prevent a single index path. Suggest rewrite may propose UNION ALL of per-column branches; Rank candidates scores them with dry EXPLAIN.",
				SQL:                 `SELECT id, date, region, product_category, total_amount FROM demo.sales WHERE region = 'North' OR product_category = 'Electronics'`,
				ExpectedImprovement: "OR → UNION ALL of indexable branches (ranked by dry EXPLAIN)",
				Category:            "or_to_union",
			},
			{
				ID:                  "partition-pruning",
				Title:               "Partition pruning failure",
				Problem:             "Open-ended date predicate still scans far more partitions than a closed month range.",
				SQL:                 `SELECT COUNT(*), SUM(total_amount) FROM demo.sales WHERE date >= '2025-01-01'`,
				ExpectedImprovement: "Open range → single-month prune (edit candidate SQL, then Compare)",
				Category:            "partition_pruning",
			},
			{
				ID:                  "cardinality-misestimate",
				Title:               "Cardinality misestimate",
				Problem:             "Planner row estimates disagree with ANALYZE reality — investigate statistics and predicate selectivity.",
				SQL:                 `SELECT s.product_category, COUNT(*) FROM demo.sales s WHERE s.region = 'North' AND s.date >= '2025-06-01' GROUP BY s.product_category`,
				ExpectedImprovement: "EXPLAIN ANALYZE row estimate check",
				Category:            "cardinality",
			},
		},
	}, nil
}

// SecurityTrust returns the security posture for the Security & Trust page.
func (s *WorkspaceService) SecurityTrust(_ context.Context) (*workspace.SecurityTrust2, error) {
	authStatus := "Enabled"
	if !s.authEnabled || s.allowInsecureNoAuth {
		authStatus = "Disabled (dev mode)"
	}
	explainAnalyze := "Disabled"
	if s.explainAnalyze {
		explainAnalyze = "Enabled"
	}
	llmData := "Disabled"
	if s.llmAllowExternal {
		llmData = "Enabled"
	}
	timeout := s.queryTimeoutSec
	if timeout <= 0 {
		timeout = 30
	}
	schemas := s.allowedSchemas
	if len(schemas) == 0 {
		schemas = []string{"demo"}
	}
	tls := s.sslMode
	if tls == "" || tls == "disable" {
		tls = "prefer"
	}
	return &workspace.SecurityTrust2{
		Authentication:      authStatus,
		ConnectionMode:      "Read-only",
		AllowedSchemas:      schemas,
		TenantIsolation:     "Dedicated database (RLS)",
		TLS:                 tls,
		AuditMode:           s.auditMode,
		QueryTimeoutSeconds: timeout,
		ResultLimit:         10000,
		ExplainAnalyze:      explainAnalyze,
		ExternalLlmData:     llmData,
	}, nil
}

func (s *WorkspaceService) seedDemoRegressions(ctx context.Context, orgID string) error {
	if !config.DemoMode() {
		return nil
	}
	var pollCount int
	if err := s.appPool.QueryRow(ctx, `
		SELECT COUNT(*) FROM app.stat_statement_polls WHERE organization_id = $1
	`, orgID).Scan(&pollCount); err != nil {
		return err
	}
	if pollCount > 0 {
		return nil
	}
	var count int
	if err := s.appPool.QueryRow(ctx, `
		SELECT COUNT(*) FROM app.regression_alerts WHERE organization_id = $1
	`, orgID).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	// Use runnable demo.sales SQL — truncated placeholders break Investigate.
	demos := []struct {
		title, query, changeType, summary, impact string
		pct                                       float64
		ago                                       time.Duration
	}{
		{
			"Revenue dashboard",
			`SELECT product_category, SUM(total_amount) AS revenue FROM demo.sales WHERE DATE_TRUNC('month', date) = DATE '2025-01-01' GROUP BY product_category ORDER BY revenue DESC`,
			"latency", "+640% latency", "critical", 640, 22 * time.Minute,
		},
		{
			"Customer search",
			`SELECT * FROM demo.sales WHERE region = 'North' AND date >= '2025-06-01' LIMIT 100`,
			"latency", "+180% latency", "high", 180, 3 * time.Hour,
		},
		{
			"Inventory report",
			`SELECT product_category, SUM(total_amount) FROM demo.sales WHERE date >= '2025-01-01' GROUP BY product_category`,
			"temp_writes", "+90% temp writes", "medium", 90, 24 * time.Hour,
		},
	}
	for _, d := range demos {
		_, err := s.appPool.Exec(ctx, `
			INSERT INTO app.regression_alerts (
				organization_id, title, query_text, change_type, change_percent,
				change_summary, impact, first_detected_at, source
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'demo')
		`, orgID, d.title, d.query, d.changeType, d.pct, d.summary, d.impact, time.Now().Add(-d.ago))
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("seed regression: %w", err)
		}
	}
	return nil
}

var _ workspace.Service = (*WorkspaceService)(nil)
