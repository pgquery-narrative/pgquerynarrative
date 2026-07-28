package service

import (
	"context"
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/api/gen/queries"
	"github.com/pgquerynarrative/pgquerynarrative/app/apilog"
	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/app/db"
)

// RegressionPollerConfig tunes background regression detection.
type RegressionPollerConfig struct {
	Enabled              bool
	Interval             time.Duration
	MeanTimeThresholdPct float64 // alert when mean time rises by at least this %
	CriticalThresholdPct float64
	HighThresholdPct     float64
	RetentionDays        int
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

	for _, row := range stats.Items {
		queryID := ""
		if row.Queryid != nil {
			queryID = *row.Queryid
		}
		if queryID == "" {
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
	}

	prevPollID, err := p.previousPollID(ctx, appDB, orgID, pollID)
	if err != nil || prevPollID == "" {
		return err
	}
	return p.detectRegressions(ctx, appDB, orgID, prevPollID, pollID)
}

func (p *RegressionPoller) previousPollID(ctx context.Context, appDB db.DB, orgID, currentPollID string) (string, error) {
	var prevID string
	err := appDB.QueryRow(ctx, `
		SELECT id::text FROM app.stat_statement_polls
		WHERE organization_id = $1 AND id::text <> $2
		ORDER BY captured_at DESC
		LIMIT 1
	`, orgID, currentPollID).Scan(&prevID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return "", nil
		}
		return "", err
	}
	return prevID, nil
}

func (p *RegressionPoller) detectRegressions(ctx context.Context, appDB db.DB, orgID, prevPollID, currentPollID string) error {
	rows, err := appDB.Query(ctx, `
		SELECT
			c.queryid, c.query_text,
			p.mean_time_ms AS prev_mean, c.mean_time_ms AS curr_mean,
			p.total_time_ms AS prev_total, c.total_time_ms AS curr_total,
			p.calls AS prev_calls, c.calls AS curr_calls,
			p.rows AS prev_rows, c.rows AS curr_rows
		FROM app.stat_statement_snapshots c
		JOIN app.stat_statement_snapshots p
		  ON p.poll_id = $1 AND p.queryid = c.queryid
		WHERE c.poll_id = $2
	`, prevPollID, currentPollID)
	if err != nil {
		return err
	}
	defer rows.Close()

	threshold := p.cfg.MeanTimeThresholdPct
	if threshold <= 0 {
		threshold = 50
	}

	for rows.Next() {
		var queryID, queryText string
		var prevMean, currMean, prevTotal, currTotal float64
		var prevCalls, currCalls, prevRows, currRows int64
		if err := rows.Scan(&queryID, &queryText, &prevMean, &currMean, &prevTotal, &currTotal, &prevCalls, &currCalls, &prevRows, &currRows); err != nil {
			return err
		}
		if prevMean <= 0 && prevTotal <= 0 {
			continue
		}

		meanPct := pctChange(prevMean, currMean)
		totalPct := pctChange(prevTotal, currTotal)
		callsPct := pctChange(float64(prevCalls), float64(currCalls))

		var changeType, summary string
		var changePct float64

		switch {
		case meanPct >= threshold:
			changeType = "latency"
			changePct = meanPct
			summary = fmt.Sprintf("+%.0f%% mean latency", meanPct)
		case totalPct >= threshold*1.5:
			changeType = "total_time"
			changePct = totalPct
			summary = fmt.Sprintf("+%.0f%% total database time", totalPct)
		case callsPct >= 200:
			changeType = "calls"
			changePct = callsPct
			summary = fmt.Sprintf("+%.0f%% calls", callsPct)
		default:
			prevRPC := rowsPerCall(prevRows, prevCalls)
			currRPC := rowsPerCall(currRows, currCalls)
			if prevRPC > 0 && math.Abs(pctChange(prevRPC, currRPC)) >= 80 {
				changeType = "rows"
				changePct = pctChange(prevRPC, currRPC)
				summary = fmt.Sprintf("+%.0f%% rows per execution", changePct)
			} else {
				continue
			}
		}

		impact := classifyImpact(changePct, p.cfg)
		title := regressionTitle(queryText)
		if err := p.openAlert(ctx, appDB, orgID, queryID, title, queryText, changeType, changePct, summary, impact); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (p *RegressionPoller) openAlert(ctx context.Context, appDB db.DB, orgID, queryID, title, queryText, changeType string, changePct float64, summary, impact string) error {
	var exists bool
	_ = appDB.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM app.regression_alerts
			WHERE organization_id = $1 AND queryid = $2 AND acknowledged_at IS NULL
		)
	`, orgID, queryID).Scan(&exists)
	if exists {
		return nil
	}
	_, err := appDB.Exec(ctx, `
		INSERT INTO app.regression_alerts (
			organization_id, title, query_text, queryid, change_type,
			change_percent, change_summary, impact, source, connection_id
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'poller', 'default')
	`, orgID, title, truncateQuery(queryText, 500), queryID, changeType, changePct, summary, impact)
	return err
}

func (p *RegressionPoller) cleanupOldPolls(ctx context.Context) {
	retention := p.cfg.RetentionDays
	if retention <= 0 {
		retention = 7
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
