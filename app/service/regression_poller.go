package service

import (
	"context"
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/api/gen/queries"
	"github.com/pgquerynarrative/pgquerynarrative/app/apilog"
	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/app/db"
	"github.com/pgquerynarrative/pgquerynarrative/app/queryrunner"
)

// RegressionPollerConfig tunes background regression detection.
type RegressionPollerConfig struct {
	Enabled              bool
	Interval             time.Duration
	MeanTimeThresholdPct float64 // alert when mean time rises by at least this % over baseline
	CriticalThresholdPct float64
	HighThresholdPct     float64
	RetentionDays        int
	// BaselineWindowDays bounds how far back the rolling baseline reaches.
	// Defaults to RetentionDays (or 14).
	BaselineWindowDays int
	// MinBaselinePolls is the minimum number of prior polls a query needs
	// before it can raise an alert. Default 3.
	MinBaselinePolls int
}

func (c RegressionPollerConfig) baselineWindowDays() int {
	if c.BaselineWindowDays > 0 {
		return c.BaselineWindowDays
	}
	if c.RetentionDays > 0 {
		return c.RetentionDays
	}
	return 14
}

// RegressionPoller captures pg_stat_statements snapshots and opens regression alerts.
type RegressionPoller struct {
	rawPool    *pgxpool.Pool
	queriesSvc *QueriesService
	cfg        RegressionPollerConfig
}

// NewRegressionPoller creates a regression poller.
func NewRegressionPoller(rawPool *pgxpool.Pool, queriesSvc *QueriesService, cfg RegressionPollerConfig) *RegressionPoller {
	return &RegressionPoller{rawPool: rawPool, queriesSvc: queriesSvc, cfg: cfg}
}

// StartRegressionPollerLoop runs snapshot + detection until ctx is done.
func StartRegressionPollerLoop(ctx context.Context, poller *RegressionPoller) {
	if poller == nil || !poller.cfg.Enabled || poller.rawPool == nil || poller.queriesSvc == nil {
		return
	}
	interval := poller.cfg.Interval
	if interval <= 0 {
		interval = 15 * time.Minute
	}
	go func() {
		// Initial poll shortly after startup.
		poller.pollAllOrgs(ctx)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				poller.pollAllOrgs(ctx)
			}
		}
	}()
}

func (p *RegressionPoller) pollAllOrgs(ctx context.Context) {
	rows, err := p.rawPool.Query(ctx, `SELECT id::text FROM app.organizations ORDER BY created_at`)
	if err != nil {
		apilog.ValidationError("regression_poller", "list_orgs", err.Error())
		return
	}
	defer rows.Close()
	for rows.Next() {
		var orgID string
		if err := rows.Scan(&orgID); err != nil {
			continue
		}
		if err := p.pollOrg(ctx, orgID); err != nil {
			log.Printf("regression poller org %s: %v", orgID, err)
		}
	}
	_ = rows.Err()
	p.cleanupOldPolls(ctx)
}

func (p *RegressionPoller) pollOrg(ctx context.Context, orgID string) error {
	ctx = auth.WithPrincipal(ctx, auth.Principal{UserID: "regression-poller", OrgID: orgID, Role: auth.RoleAdmin})
	if !p.queriesSvc.statStatementsEnabled {
		return nil
	}

	stats, err := p.queriesSvc.StatStatements(ctx, &queries.StatStatementsPayload{
		OrderBy: "total_time",
		Limit:   50,
	})
	if err != nil || stats == nil || len(stats.Items) == 0 {
		return err
	}

	appDB := db.NewOrgScoped(p.rawPool)
	var pollID string
	if err := appDB.QueryRow(ctx, `
		INSERT INTO app.stat_statement_polls (organization_id, connection_id)
		VALUES ($1, 'default')
		RETURNING id::text
	`, orgID).Scan(&pollID); err != nil {
		return fmt.Errorf("insert poll: %w", err)
	}

	stored := 0
	for _, row := range stats.Items {
		queryID := ""
		if row.Queryid != nil {
			queryID = *row.Queryid
		}
		// Never store PgQueryNarrative's own analysis traffic — it is not an
		// application workload and would otherwise seed the next poll's noise.
		if queryID == "" || isSelfObservedStatement(row.Query) {
			continue
		}
		_, err := appDB.Exec(ctx, `
			INSERT INTO app.stat_statement_snapshots (
				poll_id, queryid, query_text, calls, mean_time_ms, total_time_ms, rows
			) VALUES ($1, $2, $3, $4, $5, $6, $7)
		`, pollID, queryID, row.Query, row.Calls, row.MeanTimeMs, row.TotalTimeMs, row.Rows)
		if err != nil {
			return fmt.Errorf("insert snapshot: %w", err)
		}
		stored++
	}
	if stored == 0 {
		return nil
	}

	if err := p.detectRegressions(ctx, appDB, orgID, pollID); err != nil {
		return err
	}
	return p.reconcileAppliedFixes(ctx, appDB, orgID)
}

// EvaluateLatestPoll re-runs regression detection and applied-fix reconciliation
// for an org against its most recent snapshot poll, without capturing a new one.
// Exposed for tests and a possible "re-evaluate now" operator action.
func (p *RegressionPoller) EvaluateLatestPoll(ctx context.Context, orgID string) error {
	appDB := db.NewOrgScoped(p.rawPool)
	ctx = auth.WithPrincipal(ctx, auth.Principal{UserID: "regression-poller", OrgID: orgID, Role: auth.RoleAdmin})
	var pollID string
	if err := appDB.QueryRow(ctx, `
		SELECT id::text FROM app.stat_statement_polls
		WHERE organization_id = $1 ORDER BY captured_at DESC LIMIT 1
	`, orgID).Scan(&pollID); err != nil {
		return err
	}
	if err := p.detectRegressions(ctx, appDB, orgID, pollID); err != nil {
		return err
	}
	return p.reconcileAppliedFixes(ctx, appDB, orgID)
}

// detectRegressions compares every query in the current poll against a rolling
// baseline (median mean-time / p90 total-time / avg calls+rows over the recent
// window), opens or refreshes alerts for regressions, and auto-resolves open
// alerts whose query has returned to baseline.
func (p *RegressionPoller) detectRegressions(ctx context.Context, appDB db.DB, orgID, currentPollID string) error {
	rows, err := appDB.Query(ctx, `
		WITH baseline AS (
			SELECT s.queryid,
			       percentile_cont(0.5) WITHIN GROUP (ORDER BY s.mean_time_ms)  AS base_mean,
			       percentile_cont(0.9) WITHIN GROUP (ORDER BY s.total_time_ms) AS base_total,
			       avg(s.calls) AS base_calls,
			       avg(s.rows)  AS base_rows,
			       count(*)     AS base_polls
			FROM app.stat_statement_snapshots s
			JOIN app.stat_statement_polls b ON b.id = s.poll_id
			WHERE b.organization_id = $1
			  AND b.id <> $2
			  AND b.captured_at >= now() - ($3::text || ' days')::interval
			GROUP BY s.queryid
		)
		SELECT c.queryid, c.query_text,
		       c.mean_time_ms, c.total_time_ms, c.calls, c.rows,
		       bl.base_mean, bl.base_total, bl.base_calls, bl.base_rows, bl.base_polls
		FROM app.stat_statement_snapshots c
		JOIN baseline bl ON bl.queryid = c.queryid
		WHERE c.poll_id = $2
	`, orgID, currentPollID, p.cfg.baselineWindowDays())
	if err != nil {
		return err
	}
	defer rows.Close()

	type seen struct {
		cur      queryStats
		base     baselineStats
		queryTxt string
	}
	byQuery := map[string]seen{}

	for rows.Next() {
		var queryID, queryText string
		var cur queryStats
		var base baselineStats
		if err := rows.Scan(
			&queryID, &queryText,
			&cur.MeanMs, &cur.TotalMs, &cur.Calls, &cur.Rows,
			&base.MeanMs, &base.TotalMs, &base.Calls, &base.Rows, &base.Polls,
		); err != nil {
			return err
		}
		// Belt-and-suspenders: snapshots captured before the insert-time filter
		// existed (or via a manual poll) must not become alerts either.
		if isSelfObservedStatement(queryText) {
			continue
		}
		byQuery[queryID] = seen{cur: cur, base: base, queryTxt: queryText}

		if v, ok := evaluateRegression(cur, base, p.cfg); ok {
			impact := classifyImpact(math.Abs(v.ChangePct), p.cfg)
			if err := p.upsertAlert(ctx, appDB, orgID, queryID, regressionTitle(queryText), queryText, v, impact, cur, base.MeanMs); err != nil {
				return err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	return p.resolveRecoveredAlerts(ctx, appDB, orgID, func(queryID string) (bool, bool) {
		s, ok := byQuery[queryID]
		if !ok {
			return false, false // query not in this poll — leave the alert alone
		}
		return hasRecovered(s.cur, s.base, p.cfg), true
	})
}

// upsertAlert opens a new alert, refreshes an already-open one (bumping the
// occurrence count and the frozen stat snapshot), or re-opens with a link to the
// prior alert when the same query regressed again after recovering.
func (p *RegressionPoller) upsertAlert(ctx context.Context, appDB db.DB, orgID, queryID, title, queryText string, v regressionVerdict, impact string, cur queryStats, baselineMean float64) error {
	var openID, resolvedID string
	_ = appDB.QueryRow(ctx, `
		SELECT
			COALESCE((SELECT id::text FROM app.regression_alerts
			          WHERE organization_id = $1 AND queryid = $2
			            AND acknowledged_at IS NULL AND resolved_at IS NULL
			          ORDER BY first_detected_at DESC LIMIT 1), ''),
			COALESCE((SELECT id::text FROM app.regression_alerts
			          WHERE organization_id = $1 AND queryid = $2
			            AND resolved_at IS NOT NULL
			          ORDER BY resolved_at DESC LIMIT 1), '')
	`, orgID, queryID).Scan(&openID, &resolvedID)

	if openID != "" {
		_, err := appDB.Exec(ctx, `
			UPDATE app.regression_alerts SET
				last_seen_at = now(),
				occurrences = occurrences + 1,
				change_type = $3, change_summary = $4,
				change_percent = GREATEST(COALESCE(change_percent, 0), $5),
				impact = $6,
				calls = $7, mean_time_ms = $8, total_time_ms = $9, rows_count = $10
			WHERE id = $1 AND organization_id = $2
		`, openID, orgID, v.ChangeType, v.Summary, math.Abs(v.ChangePct), impact,
			cur.Calls, cur.MeanMs, cur.TotalMs, cur.Rows)
		return err
	}

	var prevID any
	if resolvedID != "" {
		prevID = resolvedID
	}
	_, err := appDB.Exec(ctx, `
		INSERT INTO app.regression_alerts (
			organization_id, title, query_text, queryid, change_type,
			change_percent, change_summary, impact, source, connection_id,
			calls, mean_time_ms, total_time_ms, rows_count,
			last_seen_at, baseline_mean_time_ms, previous_alert_id
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'poller', 'default', $9, $10, $11, $12, now(), $13, $14)
	`, orgID, title, truncateQuery(queryText, queryrunner.StatStatementsQueryMaxLen), queryID,
		v.ChangeType, math.Abs(v.ChangePct), v.Summary, impact,
		cur.Calls, cur.MeanMs, cur.TotalMs, cur.Rows, baselineMean, prevID)
	return err
}

// resolveRecoveredAlerts closes open poller alerts whose query is back to
// baseline. recoveredFn returns (recovered, known) for a queryid — known=false
// means the query wasn't in the latest poll, so its alert is left untouched.
func (p *RegressionPoller) resolveRecoveredAlerts(ctx context.Context, appDB db.DB, orgID string, recoveredFn func(queryID string) (recovered, known bool)) error {
	rows, err := appDB.Query(ctx, `
		SELECT id::text, queryid FROM app.regression_alerts
		WHERE organization_id = $1 AND source = 'poller'
		  AND acknowledged_at IS NULL AND resolved_at IS NULL
		  AND queryid IS NOT NULL
	`, orgID)
	if err != nil {
		return err
	}
	var toResolve []string
	for rows.Next() {
		var id, queryID string
		if err := rows.Scan(&id, &queryID); err != nil {
			rows.Close()
			return err
		}
		if recovered, known := recoveredFn(queryID); known && recovered {
			toResolve = append(toResolve, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range toResolve {
		if _, err := appDB.Exec(ctx, `
			UPDATE app.regression_alerts SET resolved_at = now()
			WHERE id = $1 AND organization_id = $2 AND resolved_at IS NULL
		`, id, orgID); err != nil {
			return err
		}
	}
	return nil
}

// fixConfirmDropPct is how much the mean latency must fall from the apply-time
// baseline for a shipped fix to be marked "confirmed".
const fixConfirmDropPct = 20.0

// fixRegressGraceHours is how long after apply a fix that shows no improvement
// waits before being marked "regressed".
const fixRegressGraceHours = 72

// reconcileAppliedFixes re-measures investigations whose fix was marked applied:
// it looks up the current pg_stat_statements mean for the linked query and, when
// it has dropped enough, marks the investigation confirmed — or regressed if no
// improvement shows after a grace period. This is what closes the loop.
func (p *RegressionPoller) reconcileAppliedFixes(ctx context.Context, appDB db.DB, orgID string) error {
	_, err := appDB.Exec(ctx, `
		WITH latest_poll AS (
			SELECT id FROM app.stat_statement_polls
			WHERE organization_id = $1
			ORDER BY captured_at DESC LIMIT 1
		),
		current_mean AS (
			SELECT s.queryid, s.mean_time_ms
			FROM app.stat_statement_snapshots s
			JOIN latest_poll lp ON lp.id = s.poll_id
		),
		applied AS (
			SELECT i.id AS investigation_id,
			       i.fix_baseline_mean_ms AS baseline,
			       i.fix_applied_at AS applied_at,
			       cm.mean_time_ms AS current_mean
			FROM app.investigations i
			JOIN app.regression_alerts ra
			  ON ra.investigation_id = i.id AND ra.organization_id = i.organization_id
			JOIN current_mean cm ON cm.queryid = ra.queryid
			WHERE i.organization_id = $1
			  AND i.fix_status = 'applied'
			  AND i.fix_baseline_mean_ms IS NOT NULL
			  AND ra.queryid IS NOT NULL
		)
		UPDATE app.investigations i SET
			fix_confirmed_mean_ms = a.current_mean,
			fix_measured_at = now(),
			fix_status = CASE
				WHEN a.current_mean <= a.baseline * (1 - $2::float / 100) THEN 'confirmed'
				WHEN a.applied_at < now() - ($3::text || ' hours')::interval
				     AND a.current_mean >= a.baseline * 0.95 THEN 'regressed'
				ELSE 'applied'
			END,
			updated_at = now()
		FROM applied a
		WHERE i.id = a.investigation_id AND i.organization_id = $1
	`, orgID, fixConfirmDropPct, fixRegressGraceHours)
	return err
}

func (p *RegressionPoller) cleanupOldPolls(ctx context.Context) {
	retention := p.cfg.RetentionDays
	if retention <= 0 {
		retention = 14
	}
	_, err := p.rawPool.Exec(ctx, `
		DELETE FROM app.stat_statement_polls
		WHERE captured_at < NOW() - ($1::text || ' days')::interval
	`, retention)
	if err != nil {
		apilog.ValidationError("regression_poller", "cleanup", err.Error())
	}
}

func pctChange(before, after float64) float64 {
	if before <= 0 {
		return 0
	}
	return ((after - before) / before) * 100
}

func rowsPerCall(rows, calls int64) float64 {
	if calls <= 0 {
		return 0
	}
	return float64(rows) / float64(calls)
}

func classifyImpact(changePct float64, cfg RegressionPollerConfig) string {
	critical := cfg.CriticalThresholdPct
	if critical <= 0 {
		critical = 200
	}
	high := cfg.HighThresholdPct
	if high <= 0 {
		high = 100
	}
	if changePct >= critical {
		return "critical"
	}
	if changePct >= high {
		return "high"
	}
	return "medium"
}

func regressionTitle(query string) string {
	q := strings.TrimSpace(query)
	if len(q) > 48 {
		q = q[:45] + "..."
	}
	if q == "" {
		return "Query regression"
	}
	return q
}

func truncateQuery(q string, max int) string {
	q = strings.TrimSpace(q)
	if len(q) <= max {
		return q
	}
	return q[:max-3] + "..."
}
