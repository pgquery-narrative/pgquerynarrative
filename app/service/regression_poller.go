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

// pollOrg polls every analytical connection the org is authorized to read
// pg_stat_statements from. Each connection is snapshotted and evaluated
// independently.
func (p *RegressionPoller) pollOrg(ctx context.Context, orgID string) error {
	ctx = auth.WithPrincipal(ctx, auth.Principal{UserID: "regression-poller", OrgID: orgID, Role: auth.RoleAdmin})
	if !p.queriesSvc.statStatementsEnabled {
		return nil
	}

	connIDs := p.queriesSvc.connectionIDs()
	if p.queriesSvc.authz != nil {
		connIDs = p.queriesSvc.authz.AllowedConnections(ctx, orgID, "regression-poller", auth.RoleAdmin, connIDs, auth.ActionStats)
	}

	var firstErr error
	for _, connID := range connIDs {
		if err := p.pollOrgConnection(ctx, orgID, connID); err != nil {
			log.Printf("regression poller org %s conn %s: %v", orgID, connID, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// pollOrgConnection snapshots and evaluates one (org, connection). A session
// advisory lock keyed on the pair makes only one app replica run a given
// (org, connection) cycle at a time; other replicas skip it silently.
func (p *RegressionPoller) pollOrgConnection(ctx context.Context, orgID, connID string) error {
	lockConn, err := p.rawPool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer lockConn.Release()

	lockKey := "regression-poll:" + orgID + ":" + connID
	var got bool
	if err := lockConn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1, 0))`, lockKey).Scan(&got); err != nil {
		return err
	}
	if !got {
		return nil // another replica holds this (org, connection) — skip
	}
	defer func() {
		_, _ = lockConn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, lockKey)
	}()

	connIDPtr := connID
	stats, err := p.queriesSvc.StatStatements(ctx, &queries.StatStatementsPayload{
		OrderBy:      "total_time",
		Limit:        50,
		ConnectionID: &connIDPtr,
	})
	if err != nil || stats == nil || len(stats.Items) == 0 {
		return err
	}

	appDB := db.NewOrgScoped(p.rawPool)
	var pollID string
	if err := appDB.QueryRow(ctx, `
		INSERT INTO app.stat_statement_polls (organization_id, connection_id)
		VALUES ($1, $2)
		RETURNING id::text
	`, orgID, connID).Scan(&pollID); err != nil {
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

	if err := p.detectRegressions(ctx, appDB, orgID, connID, pollID); err != nil {
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
	var pollID, connID string
	if err := appDB.QueryRow(ctx, `
		SELECT id::text, connection_id FROM app.stat_statement_polls
		WHERE organization_id = $1 ORDER BY captured_at DESC LIMIT 1
	`, orgID).Scan(&pollID, &connID); err != nil {
		return err
	}
	if err := p.detectRegressions(ctx, appDB, orgID, connID, pollID); err != nil {
		return err
	}
	return p.reconcileAppliedFixes(ctx, appDB, orgID)
}

// detectRegressions compares the current poll's *interval* — the deltas of the
// cumulative pg_stat_statements counters since the previous poll — against a
// rolling baseline of the query's recent intervals (median interval mean-time /
// p90 interval DB-time / avg interval calls+rows). Intervals spanning a counter
// reset (negative delta) or with no calls are dropped. It opens or refreshes
// alerts for regressions and auto-resolves open alerts whose query is back to
// baseline.
func (p *RegressionPoller) detectRegressions(ctx context.Context, appDB db.DB, orgID, connID, currentPollID string) error {
	rows, err := appDB.Query(ctx, `
		WITH windowed AS (
			SELECT s.queryid, s.query_text, b.captured_at,
			       (b.id = $2) AS is_current,
			       s.calls          - lag(s.calls)          OVER w AS d_calls,
			       s.total_time_ms  - lag(s.total_time_ms)  OVER w AS d_total,
			       s.rows           - lag(s.rows)           OVER w AS d_rows
			FROM app.stat_statement_snapshots s
			JOIN app.stat_statement_polls b ON b.id = s.poll_id
			WHERE b.organization_id = $1
			  AND b.connection_id = $4
			  AND b.captured_at >= now() - make_interval(days => $3)
			WINDOW w AS (PARTITION BY s.queryid ORDER BY b.captured_at)
		),
		intervals AS (
			-- one row per (queryid, interval); drop the first snapshot (no lag),
			-- counter-reset intervals (negative delta) and no-traffic intervals.
			SELECT queryid, query_text, captured_at, is_current,
			       d_calls, d_total, d_rows,
			       d_total / d_calls AS interval_mean
			FROM windowed
			WHERE d_calls IS NOT NULL AND d_calls > 0 AND d_total >= 0 AND d_rows >= 0
		),
		baseline AS (
			SELECT queryid,
			       percentile_cont(0.5) WITHIN GROUP (ORDER BY interval_mean) AS base_mean,
			       percentile_cont(0.9) WITHIN GROUP (ORDER BY d_total)       AS base_total,
			       avg(d_calls) AS base_calls,
			       avg(d_rows)  AS base_rows,
			       count(*)     AS base_intervals
			FROM intervals
			WHERE NOT is_current
			GROUP BY queryid
		),
		current AS (
			SELECT DISTINCT ON (queryid)
			       queryid, query_text, d_calls, d_total, d_rows, interval_mean
			FROM intervals
			WHERE is_current
			ORDER BY queryid, captured_at DESC
		)
		SELECT c.queryid, c.query_text,
		       c.interval_mean, c.d_total, c.d_calls, c.d_rows,
		       bl.base_mean, bl.base_total, bl.base_calls, bl.base_rows, bl.base_intervals
		FROM current c
		JOIN baseline bl ON bl.queryid = c.queryid
	`, orgID, currentPollID, p.cfg.baselineWindowDays(), connID)
	if err != nil {
		return err
	}
	defer rows.Close()

	type seen struct {
		cur      intervalStats
		base     intervalBaseline
		queryTxt string
	}
	byQuery := map[string]seen{}

	for rows.Next() {
		var queryID, queryText string
		var cur intervalStats
		var base intervalBaseline
		if err := rows.Scan(
			&queryID, &queryText,
			&cur.MeanMs, &cur.DeltaTotalMs, &cur.DeltaCalls, &cur.DeltaRows,
			&base.MeanMs, &base.DeltaTotalMs, &base.DeltaCalls, &base.DeltaRows, &base.Intervals,
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
			if err := p.upsertAlert(ctx, appDB, orgID, connID, queryID, regressionTitle(queryText), queryText, v, impact, cur, base.MeanMs); err != nil {
				return err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	return p.resolveRecoveredAlerts(ctx, appDB, orgID, connID, func(queryID string) (bool, bool) {
		s, ok := byQuery[queryID]
		if !ok {
			return false, false // query not in this poll — leave the alert alone
		}
		return hasRecovered(s.cur, s.base, p.cfg), true
	})
}

// upsertAlert opens a new alert for (org, connection, query), refreshes the
// already-open one (bumping occurrences and the frozen interval snapshot) via
// ON CONFLICT against the partial unique index, and links a fresh alert to the
// prior resolved one when the query regressed again after recovering.
func (p *RegressionPoller) upsertAlert(ctx context.Context, appDB db.DB, orgID, connID, queryID, title, queryText string, v regressionVerdict, impact string, cur intervalStats, baselineMean float64) error {
	var resolvedID string
	_ = appDB.QueryRow(ctx, `
		SELECT COALESCE((SELECT id::text FROM app.regression_alerts
		          WHERE organization_id = $1 AND connection_id = $2 AND queryid = $3
		            AND resolved_at IS NOT NULL
		          ORDER BY resolved_at DESC LIMIT 1), '')
	`, orgID, connID, queryID).Scan(&resolvedID)

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
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'poller', $9, $10, $11, $12, $13, now(), $14, $15)
		ON CONFLICT (organization_id, connection_id, queryid)
		  WHERE resolved_at IS NULL AND acknowledged_at IS NULL AND queryid IS NOT NULL
		DO UPDATE SET
			last_seen_at   = now(),
			occurrences    = app.regression_alerts.occurrences + 1,
			change_type    = EXCLUDED.change_type,
			change_summary = EXCLUDED.change_summary,
			change_percent = GREATEST(COALESCE(app.regression_alerts.change_percent, 0), EXCLUDED.change_percent),
			impact         = EXCLUDED.impact,
			calls          = EXCLUDED.calls,
			mean_time_ms   = EXCLUDED.mean_time_ms,
			total_time_ms  = EXCLUDED.total_time_ms,
			rows_count     = EXCLUDED.rows_count
	`, orgID, title, truncateQuery(queryText, queryrunner.StatStatementsQueryMaxLen), queryID,
		v.ChangeType, math.Abs(v.ChangePct), v.Summary, impact, connID,
		cur.DeltaCalls, cur.MeanMs, cur.DeltaTotalMs, cur.DeltaRows, baselineMean, prevID)
	return err
}

// resolveRecoveredAlerts closes open poller alerts whose query is back to
// baseline. recoveredFn returns (recovered, known) for a queryid — known=false
// means the query wasn't in the latest poll, so its alert is left untouched.
func (p *RegressionPoller) resolveRecoveredAlerts(ctx context.Context, appDB db.DB, orgID, connID string, recoveredFn func(queryID string) (recovered, known bool)) error {
	rows, err := appDB.Query(ctx, `
		SELECT id::text, queryid FROM app.regression_alerts
		WHERE organization_id = $1 AND connection_id = $2 AND source = 'poller'
		  AND acknowledged_at IS NULL AND resolved_at IS NULL
		  AND queryid IS NOT NULL
	`, orgID, connID)
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
// it computes the linked query's latest *interval* mean latency (delta of the
// cumulative counters between the two most recent polls) and, when it has
// dropped enough below the interval mean captured at apply time, marks the
// investigation confirmed — or regressed if no improvement shows after a grace
// period. This is what closes the loop.
func (p *RegressionPoller) reconcileAppliedFixes(ctx context.Context, appDB db.DB, orgID string) error {
	_, err := appDB.Exec(ctx, `
		WITH ranked AS (
			SELECT s.queryid,
			       s.total_time_ms - lag(s.total_time_ms) OVER w AS d_total,
			       s.calls         - lag(s.calls)         OVER w AS d_calls,
			       row_number() OVER (PARTITION BY s.queryid ORDER BY p.captured_at DESC) AS rn
			FROM app.stat_statement_snapshots s
			JOIN app.stat_statement_polls p ON p.id = s.poll_id
			WHERE p.organization_id = $1
			WINDOW w AS (PARTITION BY s.queryid ORDER BY p.captured_at)
		),
		current_mean AS (
			SELECT queryid, d_total / d_calls AS mean_time_ms
			FROM ranked
			WHERE rn = 1 AND d_calls > 0 AND d_total >= 0
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
				WHEN a.applied_at < now() - make_interval(hours => $3)
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
		WHERE captured_at < NOW() - make_interval(days => $1)
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
