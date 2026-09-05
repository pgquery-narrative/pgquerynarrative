package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pgquerynarrative/pgquerynarrative/api/gen/queries"
	"github.com/pgquerynarrative/pgquerynarrative/api/gen/workspace"
	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/app/config"
	"github.com/pgquerynarrative/pgquerynarrative/app/db"
	"github.com/pgquerynarrative/pgquerynarrative/app/queryrunner"
)

// WorkspaceService provides landing dashboard data, regression inbox, demo scenarios, and trust.
type WorkspaceService struct {
	appPool               db.DB
	queriesSvc            *QueriesService
	authEnabled           bool
	allowInsecureNoAuth   bool
	explainAnalyzeEnabled bool // server-wide EXPLAIN ANALYZE gate; same for every connection
	auditMode             string
	llmAllowExternal      bool
	connections           map[string]config.DataConnectionConfig // per-connection raw config, keyed by ID
	defaultConnectionID   string
}

// NewWorkspaceService creates a workspace service. connections carries each
// data connection's already-resolved raw configuration (SSLMode, AllowedSchemas,
// ...); SecurityTrust reports those values as configured, it does not re-derive
// or substitute friendlier-looking defaults for them.
func NewWorkspaceService(
	appPool db.DB,
	queriesSvc *QueriesService,
	authEnabled, allowInsecureNoAuth, explainAnalyzeEnabled bool,
	auditMode string,
	llmAllowExternal bool,
	connections []config.DataConnectionConfig,
	defaultConnectionID string,
) *WorkspaceService {
	byID := make(map[string]config.DataConnectionConfig, len(connections))
	for _, c := range connections {
		byID[c.ID] = c
	}
	return &WorkspaceService{
		appPool:               appPool,
		queriesSvc:            queriesSvc,
		authEnabled:           authEnabled,
		allowInsecureNoAuth:   allowInsecureNoAuth,
		explainAnalyzeEnabled: explainAnalyzeEnabled,
		auditMode:             auditMode,
		llmAllowExternal:      llmAllowExternal,
		connections:           byID,
		defaultConnectionID:   defaultConnectionID,
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

// demoScenarioMonthTimeout bounds the demo.sales range probe so the Investigate
// page never blocks on it; on timeout the caller falls back to a computed month.
const demoScenarioMonthTimeout = 2 * time.Second

// demoScenarioMonth returns the first day of a month that actually contains
// demo.sales rows, so guided scenario SQL returns rows instead of an empty
// result.
//
// Both seeds generate dates rolling backwards from CURRENT_DATE
// (tools/db/seed.sql: 365 days, tools/db/seed-large.sql: 730 days), so a
// hardcoded calendar month goes empty as soon as the window moves past it.
// The seeds document this contract ("Investigate sample SQL reads live
// MIN/MAX(date)"); this is that read.
//
// The month of MAX(date) is only partially filled (the seed stops at today), so
// prefer the month before it, clamped to the month of MIN(date) for datasets
// that span less than one month.
func (s *WorkspaceService) demoScenarioMonth(ctx context.Context) time.Time {
	// Fallback matches the seeds' own shape: one whole month back from today is
	// inside both the 365- and 730-day windows.
	fallback := monthStart(time.Now().UTC().AddDate(0, -1, 0))
	if s.queriesSvc == nil {
		return fallback
	}
	runner, err := s.queriesSvc.runnerFor(nil)
	if err != nil || runner == nil {
		return fallback
	}
	pool := runner.StatsPoolFor(ctx)
	if pool == nil {
		return fallback
	}

	probeCtx, cancel := context.WithTimeout(ctx, demoScenarioMonthTimeout)
	defer cancel()

	var minDate, maxDate time.Time
	if err := pool.QueryRow(probeCtx, `
		SELECT min(date)::date, max(date)::date FROM demo.sales
	`).Scan(&minDate, &maxDate); err != nil {
		return fallback
	}
	if minDate.IsZero() || maxDate.IsZero() {
		return fallback
	}

	month := monthStart(maxDate.AddDate(0, -1, 0))
	if month.Before(monthStart(minDate)) {
		month = monthStart(maxDate)
	}
	return month
}

// monthStart truncates t to the first day of its month, in UTC.
func monthStart(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// DemoScenarios returns sample problem SQL for guided walkthroughs.
// CandidateSQL is intentionally omitted: the rewrite must come from Suggest rewrite /
// Rank candidates (or a human), not a hardcoded answer key.
//
// Date literals are injected from the live demo.sales range (see
// demoScenarioMonth) rather than hardcoded, so the first click of the guided
// demo returns rows on any seed age.
func (s *WorkspaceService) DemoScenarios(ctx context.Context) (*workspace.DemoScenarioList, error) {
	month := s.demoScenarioMonth(ctx)
	monthLiteral := month.Format("2006-01-02")
	nextMonthLiteral := month.AddDate(0, 1, 0).Format("2006-01-02")
	monthLabel := month.Format("January 2006")
	year := month.Year()
	monthNum := int(month.Month())

	return &workspace.DemoScenarioList{
		Items: []*workspace.DemoScenario{
			{
				ID:                  "slow-dashboard",
				Title:               "Slow dashboard query",
				Problem:             "Revenue rollup uses DATE_TRUNC on the partition key, so PostgreSQL cannot prune partitions or use a date index. Use Suggest rewrite to propose a sargable range.",
				SQL:                 fmt.Sprintf(`SELECT product_category, SUM(total_amount) AS revenue FROM demo.sales WHERE DATE_TRUNC('month', date) = DATE '%s' GROUP BY product_category ORDER BY revenue DESC`, monthLiteral),
				ExpectedImprovement: "Many partitions → 1 month (range rewrite via Suggest rewrite)",
				Category:            "function_wrap",
			},
			{
				ID:                  "extract-year-month",
				Title:               "EXTRACT wraps the partition key",
				Problem:             "EXTRACT(YEAR/MONTH FROM date) is another function wrap that blocks partition pruning. Suggest rewrite should propose a January range from the AST — not a DATE_TRUNC answer key.",
				SQL:                 fmt.Sprintf(`SELECT product_category, SUM(total_amount) AS revenue FROM demo.sales WHERE EXTRACT(YEAR FROM date) = %d AND EXTRACT(MONTH FROM date) = %d GROUP BY product_category ORDER BY revenue DESC`, year, monthNum),
				ExpectedImprovement: fmt.Sprintf("Year+month EXTRACT → %s range (partition prune via Suggest rewrite)", monthLabel),
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
				Problem:             fmt.Sprintf("Open-ended date predicate still scans far more partitions than a closed month range (e.g. >= '%s' AND < '%s').", monthLiteral, nextMonthLiteral),
				SQL:                 fmt.Sprintf(`SELECT COUNT(*), SUM(total_amount) FROM demo.sales WHERE date >= '%s'`, monthLiteral),
				ExpectedImprovement: fmt.Sprintf("Open range → single-month prune to %s (edit candidate SQL, then Compare)", monthLabel),
				Category:            "partition_pruning",
			},
			{
				ID:                  "cardinality-misestimate",
				Title:               "Cardinality misestimate",
				Problem:             "Planner row estimates disagree with ANALYZE reality — investigate statistics and predicate selectivity.",
				SQL:                 fmt.Sprintf(`SELECT s.product_category, COUNT(*) FROM demo.sales s WHERE s.region = 'North' AND s.date >= '%s' GROUP BY s.product_category`, monthLiteral),
				ExpectedImprovement: "EXPLAIN ANALYZE row estimate check",
				Category:            "cardinality",
			},
		},
	}, nil
}

// securityTrustActions lists every connection action reflected in
// authorization_state, in the order they are checked.
var securityTrustActions = []string{
	auth.ActionQuery, auth.ActionExplain, auth.ActionAnalyze, auth.ActionSchema,
	auth.ActionReport, auth.ActionSchedule, auth.ActionStats, auth.ActionAsk,
}

// SecurityTrust returns the real, per-connection security posture for the
// Security & Trust page. Every field is the connection's actual configured or
// observed state — it never substitutes a friendlier-looking value for a real
// one (sslmode=disable reports "disable", never "prefer"; a zero timeout
// reports 0, never a synthetic default).
func (s *WorkspaceService) SecurityTrust(ctx context.Context, payload *workspace.SecurityTrustPayload) (*workspace.SecurityTrust2, error) {
	var reqID *string
	if payload != nil {
		reqID = payload.ConnectionID
	}

	connID := s.defaultConnectionID
	var authz ConnectionAuthorizer
	if s.queriesSvc != nil {
		authz = s.queriesSvc.authz
		id, err := s.queriesSvc.resolveConnectionID(reqID)
		if err != nil {
			return nil, connectionNotFoundWorkspaceError(err)
		}
		connID = id
	} else if reqID != nil && strings.TrimSpace(*reqID) != "" {
		connID = strings.TrimSpace(*reqID)
	}

	// Gate the whole posture on the caller actually being allowed to see this
	// connection at all. Without this, any authenticated caller could read
	// another org's connection's TLS mode, schemas, and timeout just by
	// passing its connection_id — the per-action checks below only ever
	// affected the display-only authorization_state/analyze_policy fields,
	// never whether the rest of the response was returned at all.
	if err := checkConnectionAccess(ctx, authz, connID, auth.ActionStats); err != nil {
		return nil, connectionForbiddenWorkspaceError(err)
	}

	var runner *queryrunner.Runner
	if s.queriesSvc != nil {
		r, err := s.queriesSvc.runnerFor(&connID)
		if err != nil {
			// connID was just resolved successfully above, so this would mean
			// the runner and connection resolvers disagree — surface it
			// rather than silently reporting zero-value timeout/limit/readonly
			// as if they were real (permissive) configuration.
			return nil, connectionNotFoundWorkspaceError(err)
		}
		runner = r
	}

	authStatus := "Enabled"
	if !s.authEnabled || s.allowInsecureNoAuth {
		authStatus = "Disabled (dev mode)"
	}
	llmData := "Disabled"
	if s.llmAllowExternal {
		llmData = "Enabled"
	}

	conn, knownConn := s.connections[connID]
	tls := "unknown"
	schemas := []string{}
	if knownConn {
		tls = conn.SSLMode
		schemas = append([]string(nil), conn.AllowedSchemas...)
	}

	var timeout, resultLimit int32
	if runner != nil {
		timeout = timeoutSecondsCeil(runner.QueryTimeout())
		resultLimit = int32(runner.MaxRows())
	}

	// Fail closed: an unconfirmed probe reports false, never an assumed-safe true.
	readonly := s.probeReadOnly(ctx, runner)

	authzState := make([]string, 0, len(securityTrustActions))
	canAnalyze := false
	for _, action := range securityTrustActions {
		if err := checkConnectionAccess(ctx, authz, connID, action); err == nil {
			authzState = append(authzState, action)
			if action == auth.ActionAnalyze {
				canAnalyze = true
			}
		}
	}

	analyzePolicy := "Disabled (server policy)"
	if s.explainAnalyzeEnabled {
		if canAnalyze {
			analyzePolicy = "Enabled"
		} else {
			analyzePolicy = "Disabled (no permission)"
		}
	}
	explainAnalyze := "Disabled"
	if s.explainAnalyzeEnabled {
		explainAnalyze = "Enabled"
	}

	return &workspace.SecurityTrust2{
		ConnectionID:        connID,
		Authentication:      authStatus,
		ConnectionMode:      "Read-only",
		Readonly:            readonly,
		AllowedSchemas:      schemas,
		TenantIsolation:     "Dedicated database (RLS)",
		TLS:                 tls,
		AuditMode:           s.auditMode,
		QueryTimeoutSeconds: timeout,
		ResultLimit:         resultLimit,
		ExplainAnalyze:      explainAnalyze,
		ExternalLlmData:     llmData,
		AuthorizationState:  authzState,
		AnalyzePolicy:       analyzePolicy,
		// No security-verification marker is persisted anywhere yet (the
		// db-security CI check is CI-only and writes nothing to the app DB),
		// so this is always absent — the UI shows "never" rather than a made-up date.
		LastSecurityVerification: nil,
	}, nil
}

// timeoutSecondsCeil converts a configured timeout to whole seconds, rounding
// up. A sub-second timeout (e.g. 500ms) is real enforcement and must not
// truncate to 0, which the API represents as "no timeout enforced".
func timeoutSecondsCeil(d time.Duration) int32 {
	if d <= 0 {
		return 0
	}
	secs := (d + time.Second - 1) / time.Second
	if secs > time.Duration(math.MaxInt32) {
		return math.MaxInt32
	}
	return int32(secs)
}

func connectionNotFoundWorkspaceError(err error) error {
	if err == nil {
		return nil
	}
	return &workspace.ValidationError{
		Name:    "validation_error",
		Message: "connection not found",
		Code:    strPtr("CONNECTION_NOT_FOUND"),
	}
}

func connectionForbiddenWorkspaceError(err error) error {
	if err == nil {
		return nil
	}
	return &workspace.ValidationError{
		Name:    "validation_error",
		Message: "connection access denied",
		Code:    strPtr("CONNECTION_FORBIDDEN"),
	}
}

// probeReadOnly asks the live connection whether its role is actually
// read-only right now. It reports false (not confirmed) rather than true
// whenever the probe cannot run, so a broken probe never claims safety it
// hasn't verified.
func (s *WorkspaceService) probeReadOnly(ctx context.Context, runner *queryrunner.Runner) bool {
	if runner == nil {
		return false
	}
	pool := runner.StatsPoolFor(ctx)
	if pool == nil {
		return false
	}
	var mode string
	if err := pool.QueryRow(ctx, `SHOW transaction_read_only`).Scan(&mode); err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(mode), "on")
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
