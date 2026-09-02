package integration

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/app/db"
	"github.com/pgquerynarrative/pgquerynarrative/app/service"
	"github.com/pgquerynarrative/pgquerynarrative/gen/investigations"
	"github.com/pgquerynarrative/pgquerynarrative/test/testhelpers"
)

func regressionTestPool(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	container := testhelpers.RunPostgresContainer(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })
	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	waitReady(t, ctx, connStr)
	migrationsPath, err := filepath.Abs("../../app/db/migrations")
	if err != nil {
		t.Fatal(err)
	}
	m, err := migrate.New("file://"+migrationsPath, connStr)
	if err != nil {
		t.Fatalf("migrator: %v", err)
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// seedPoll inserts one poll captured `ageMin` minutes ago with one snapshot row.
func seedPoll(t *testing.T, ctx context.Context, pool *pgxpool.Pool, org string, ageMin int, queryID, queryText string, mean, total float64, calls, rows int64) string {
	t.Helper()
	var pollID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.stat_statement_polls (organization_id, connection_id, captured_at)
		VALUES ($1, 'default', now() - make_interval(mins => $2))
		RETURNING id::text
	`, org, ageMin).Scan(&pollID); err != nil {
		t.Fatalf("seed poll: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.stat_statement_snapshots (poll_id, queryid, query_text, calls, mean_time_ms, total_time_ms, rows)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, pollID, queryID, queryText, calls, mean, total, rows); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}
	return pollID
}

func newTestPoller(pool *pgxpool.Pool) *service.RegressionPoller {
	return service.NewRegressionPoller(pool, nil, service.RegressionPollerConfig{
		Enabled:              true,
		MeanTimeThresholdPct: 50,
		MinBaselinePolls:     3,
		CriticalThresholdPct: 200,
		HighThresholdPct:     100,
		BaselineWindowDays:   14,
		RetentionDays:        14,
	})
}

func openAlertCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, org, queryID string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM app.regression_alerts
		WHERE organization_id = $1 AND queryid = $2 AND acknowledged_at IS NULL AND resolved_at IS NULL
	`, org, queryID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func TestRegressionPoller_BaselineLifecycle(t *testing.T) {
	ctx := context.Background()
	pool := regressionTestPool(t, ctx)
	org := auth.DefaultOrganizationID
	poller := newTestPoller(pool)
	const qid = "12345"
	const qtext = "SELECT * FROM orders WHERE customer_id = 42"

	// 5 baseline polls around 100ms mean with mild jitter, then a steady current poll.
	for i, mean := range []float64{96, 104, 99, 101, 103} {
		seedPoll(t, ctx, pool, org, 100-(i*10), qid, qtext, mean, mean*50, 50, 500)
	}
	seedPoll(t, ctx, pool, org, 5, qid, qtext, 108, 108*50, 50, 500)

	if err := poller.EvaluateLatestPoll(ctx, org); err != nil {
		t.Fatalf("EvaluateLatestPoll (normal): %v", err)
	}
	if n := openAlertCount(t, ctx, pool, org, qid); n != 0 {
		t.Fatalf("normal variance must not alert, got %d open alerts", n)
	}

	// Now a real regression: current mean 3x the baseline.
	regPoll := seedPoll(t, ctx, pool, org, 1, qid, qtext, 320, 320*50, 50, 500)
	_ = regPoll
	if err := poller.EvaluateLatestPoll(ctx, org); err != nil {
		t.Fatalf("EvaluateLatestPoll (regression): %v", err)
	}
	var changeType string
	var occ int
	if err := pool.QueryRow(ctx, `
		SELECT change_type, occurrences FROM app.regression_alerts
		WHERE organization_id = $1 AND queryid = $2 AND acknowledged_at IS NULL AND resolved_at IS NULL
	`, org, qid).Scan(&changeType, &occ); err != nil {
		t.Fatalf("expected one open alert: %v", err)
	}
	if changeType != "latency" || occ != 1 {
		t.Fatalf("alert change_type=%q occurrences=%d, want latency/1", changeType, occ)
	}

	// Same regression persists on the next poll → occurrence bump, still one alert.
	seedPoll(t, ctx, pool, org, 0, qid, qtext, 300, 300*50, 50, 500)
	if err := poller.EvaluateLatestPoll(ctx, org); err != nil {
		t.Fatalf("EvaluateLatestPoll (persist): %v", err)
	}
	if n := openAlertCount(t, ctx, pool, org, qid); n != 1 {
		t.Fatalf("recurring regression must stay one alert, got %d", n)
	}
	if err := pool.QueryRow(ctx, `
		SELECT occurrences FROM app.regression_alerts
		WHERE organization_id=$1 AND queryid=$2 AND resolved_at IS NULL
	`, org, qid).Scan(&occ); err != nil || occ < 2 {
		t.Fatalf("occurrences=%d err=%v, want >= 2", occ, err)
	}

	// Query recovers to baseline → alert auto-resolves.
	seedPoll(t, ctx, pool, org, 0, qid, qtext, 102, 102*50, 50, 500)
	if err := poller.EvaluateLatestPoll(ctx, org); err != nil {
		t.Fatalf("EvaluateLatestPoll (recover): %v", err)
	}
	if n := openAlertCount(t, ctx, pool, org, qid); n != 0 {
		t.Fatalf("recovered query should have no open alert, got %d", n)
	}
	var resolved int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM app.regression_alerts WHERE queryid=$1 AND resolved_at IS NOT NULL`, qid).Scan(&resolved)
	if resolved != 1 {
		t.Fatalf("expected 1 resolved alert, got %d", resolved)
	}

	// Regresses again → a NEW alert linked to the resolved one.
	seedPoll(t, ctx, pool, org, 0, qid, qtext, 340, 340*50, 50, 500)
	if err := poller.EvaluateLatestPoll(ctx, org); err != nil {
		t.Fatalf("EvaluateLatestPoll (re-regress): %v", err)
	}
	var prevID *string
	if err := pool.QueryRow(ctx, `
		SELECT previous_alert_id::text FROM app.regression_alerts
		WHERE organization_id=$1 AND queryid=$2 AND resolved_at IS NULL AND acknowledged_at IS NULL
	`, org, qid).Scan(&prevID); err != nil {
		t.Fatalf("expected a re-opened alert: %v", err)
	}
	if prevID == nil {
		t.Fatal("re-opened alert should link previous_alert_id to the resolved one")
	}
}

func TestRegressionPoller_SelfObservedNeverAlerts(t *testing.T) {
	ctx := context.Background()
	pool := regressionTestPool(t, ctx)
	org := auth.DefaultOrganizationID
	poller := newTestPoller(pool)
	const qid = "999"
	const explainText = "EXPLAIN (GENERIC_PLAN, FORMAT JSON) SELECT * FROM orders WHERE id = 42"

	for i := range []int{0, 1, 2, 3} {
		seedPoll(t, ctx, pool, org, 40-(i*10), qid, explainText, 10, 500, 50, 0)
	}
	seedPoll(t, ctx, pool, org, 0, qid, explainText, 900, 45000, 50, 0) // 90x jump

	if err := poller.EvaluateLatestPoll(ctx, org); err != nil {
		t.Fatalf("EvaluateLatestPoll: %v", err)
	}
	if n := openAlertCount(t, ctx, pool, org, qid); n != 0 {
		t.Fatalf("self-observed EXPLAIN must never alert, got %d", n)
	}
}

func TestInvestigationFix_LifecycleAndReconcile(t *testing.T) {
	ctx := context.Background()
	pool := regressionTestPool(t, ctx)
	org := auth.DefaultOrganizationID
	appDB := db.NewOrgScoped(pool)
	invSvc := service.NewInvestigationsService(appDB, nil, nil)
	reqCtx := auth.WithPrincipal(ctx, auth.Principal{UserID: "fix-test", OrgID: org, Role: auth.RoleAdmin})

	const qid = "77777"
	// An investigation whose fix is about to be marked applied.
	var invID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.investigations (organization_id, title, sql, query_fingerprint, status)
		VALUES ($1, 'Slow orders', 'SELECT * FROM orders WHERE customer_id = 42', 'fp-orders', 'complete')
		RETURNING id::text
	`, org).Scan(&invID); err != nil {
		t.Fatalf("seed investigation: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.regression_alerts (organization_id, title, query_text, queryid, change_type, change_summary, impact, source, investigation_id)
		VALUES ($1, 'Slow orders', 'SELECT * FROM orders WHERE customer_id = 42', $2, 'latency', '+200%', 'high', 'poller', $3)
	`, org, qid, invID); err != nil {
		t.Fatalf("seed linked alert: %v", err)
	}

	// Baseline the linked query at 200ms across enough polls, then mark applied.
	for i := range []int{0, 1, 2} {
		seedPoll(t, ctx, pool, org, 30-(i*10), qid, "SELECT * FROM orders WHERE customer_id = 42", 200, 10000, 50, 500)
	}

	// Invalid transition rejected.
	if _, err := invSvc.UpdateFix(reqCtx, &investigations.UpdateFixPayload{ID: invID, FixStatus: strp("confirmed")}); err == nil {
		t.Fatal("proposed -> confirmed should be rejected")
	}

	upd, err := invSvc.UpdateFix(reqCtx, &investigations.UpdateFixPayload{
		ID: invID, FixStatus: strp("applied"), FixReference: strp("https://github.com/x/y/pull/42"),
	})
	if err != nil {
		t.Fatalf("UpdateFix applied: %v", err)
	}
	if upd.FixStatus == nil || *upd.FixStatus != "applied" || upd.FixAppliedAt == nil {
		t.Fatalf("applied not recorded: %+v", upd)
	}
	if upd.FixBaselineMeanMs == nil || *upd.FixBaselineMeanMs < 150 || *upd.FixBaselineMeanMs > 250 {
		t.Fatalf("fix baseline mean not snapshotted from the linked query: %+v", upd.FixBaselineMeanMs)
	}

	// A later poll shows the query is 3x faster → reconcile marks it confirmed.
	seedPoll(t, ctx, pool, org, 0, qid, "SELECT * FROM orders WHERE customer_id = 42", 65, 3250, 50, 500)
	poller := newTestPoller(pool)
	if err := poller.EvaluateLatestPoll(ctx, org); err != nil {
		t.Fatalf("EvaluateLatestPoll (reconcile): %v", err)
	}

	got, err := invSvc.Get(reqCtx, &investigations.GetPayload{ID: invID})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.FixStatus == nil || *got.FixStatus != "confirmed" {
		t.Fatalf("expected fix_status=confirmed after re-measure, got %v", got.FixStatus)
	}
	if got.FixConfirmedMeanMs == nil || *got.FixConfirmedMeanMs > 100 {
		t.Fatalf("expected confirmed mean ~65ms, got %v", got.FixConfirmedMeanMs)
	}
}

func strp(s string) *string { return &s }
