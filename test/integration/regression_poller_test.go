package integration

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/api/gen/investigations"
	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/app/db"
	"github.com/pgquerynarrative/pgquerynarrative/app/service"
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

// cumCounters tracks a query's running pg_stat_statements totals so a test can
// describe traffic per interval and let the helper accumulate.
type cumCounters struct {
	calls   int64
	totalMs float64
	rows    int64
}

// seedPoll inserts one poll captured `ageMin` minutes ago holding one snapshot
// row with CUMULATIVE counters (as pg_stat_statements reports them).
func seedPoll(t *testing.T, ctx context.Context, pool *pgxpool.Pool, org, connID string, ageMin int, queryID, queryText string, cum cumCounters) string {
	t.Helper()
	mean := 0.0
	if cum.calls > 0 {
		mean = cum.totalMs / float64(cum.calls)
	}
	var pollID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.stat_statement_polls (organization_id, connection_id, captured_at)
		VALUES ($1, $4, now() - make_interval(mins => $2))
		RETURNING id::text
	`, org, ageMin, connID).Scan(&pollID); err != nil {
		t.Fatalf("seed poll: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.stat_statement_snapshots (poll_id, queryid, query_text, calls, mean_time_ms, total_time_ms, rows)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, pollID, queryID, queryText, cum.calls, mean, cum.totalMs, cum.rows); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}
	return pollID
}

// addInterval advances the running counters by one interval's traffic and seeds
// a poll for it.
func addInterval(t *testing.T, ctx context.Context, pool *pgxpool.Pool, org, connID string, cum *cumCounters, ageMin int, qid, qtext string, dCalls int64, dTotalMs float64, dRows int64) string {
	t.Helper()
	cum.calls += dCalls
	cum.totalMs += dTotalMs
	cum.rows += dRows
	return seedPoll(t, ctx, pool, org, connID, ageMin, qid, qtext, *cum)
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

	// Anchor snapshot (lag origin), then five ~100ms/50-call baseline intervals.
	cum := cumCounters{}
	seedPoll(t, ctx, pool, org, "default", 120, qid, qtext, cum)
	for i, dTotal := range []float64{5000, 5200, 4950, 5050, 5150} { // per-interval ms
		addInterval(t, ctx, pool, org, "default", &cum, 105-(i*15), qid, qtext, 50, dTotal, 500)
	}
	// Current interval ~108ms — mild jitter, no alert.
	addInterval(t, ctx, pool, org, "default", &cum, 20, qid, qtext, 50, 5400, 500)

	if err := poller.EvaluateLatestPoll(ctx, org); err != nil {
		t.Fatalf("EvaluateLatestPoll (normal): %v", err)
	}
	if n := openAlertCount(t, ctx, pool, org, qid); n != 0 {
		t.Fatalf("normal variance must not alert, got %d open alerts", n)
	}

	// Real regression: this interval is ~320ms/call, ~3x the baseline.
	addInterval(t, ctx, pool, org, "default", &cum, 15, qid, qtext, 50, 16000, 500)
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

	// Same regression persists next interval → occurrence bump, still one alert.
	addInterval(t, ctx, pool, org, "default", &cum, 10, qid, qtext, 50, 15000, 500)
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

	// Query recovers — this interval is back to ~102ms/call → alert auto-resolves.
	addInterval(t, ctx, pool, org, "default", &cum, 5, qid, qtext, 50, 5100, 500)
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
	addInterval(t, ctx, pool, org, "default", &cum, 1, qid, qtext, 50, 17000, 500)
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

	cum := cumCounters{}
	seedPoll(t, ctx, pool, org, "default", 60, qid, explainText, cum)
	for i := 0; i < 4; i++ {
		addInterval(t, ctx, pool, org, "default", &cum, 50-(i*10), qid, explainText, 50, 500, 0) // ~10ms/call
	}
	addInterval(t, ctx, pool, org, "default", &cum, 0, qid, explainText, 50, 45000, 0) // 90x jump

	if err := poller.EvaluateLatestPoll(ctx, org); err != nil {
		t.Fatalf("EvaluateLatestPoll: %v", err)
	}
	if n := openAlertCount(t, ctx, pool, org, qid); n != 0 {
		t.Fatalf("self-observed EXPLAIN must never alert, got %d", n)
	}
}

// The same queryid on two connections is baselined and alerted independently:
// a regression on "analytics" must not be diluted by, or leak onto, "default".
func TestRegressionPoller_PerConnectionIsolation(t *testing.T) {
	ctx := context.Background()
	pool := regressionTestPool(t, ctx)
	org := auth.DefaultOrganizationID
	poller := newTestPoller(pool)
	const qid = "55555"
	const qtext = "SELECT * FROM orders WHERE customer_id = 42"

	// "default": flat ~100ms baseline, latest poll older than the analytics one.
	defCum := cumCounters{}
	seedPoll(t, ctx, pool, org, "default", 200, qid, qtext, defCum)
	for i := 0; i < 5; i++ {
		addInterval(t, ctx, pool, org, "default", &defCum, 180-(i*20), qid, qtext, 50, 5000, 500)
	}

	// "analytics": flat ~100ms baseline, then a 3x spike as the org-latest poll.
	anCum := cumCounters{}
	seedPoll(t, ctx, pool, org, "analytics", 120, qid, qtext, anCum)
	for i := 0; i < 5; i++ {
		addInterval(t, ctx, pool, org, "analytics", &anCum, 100-(i*15), qid, qtext, 50, 5000, 500)
	}
	addInterval(t, ctx, pool, org, "analytics", &anCum, 1, qid, qtext, 50, 16000, 500) // ~320ms/call

	if err := poller.EvaluateLatestPoll(ctx, org); err != nil {
		t.Fatalf("EvaluateLatestPoll: %v", err)
	}

	var conn, changeType string
	if err := pool.QueryRow(ctx, `
		SELECT connection_id, change_type FROM app.regression_alerts
		WHERE organization_id=$1 AND queryid=$2 AND resolved_at IS NULL AND acknowledged_at IS NULL
	`, org, qid).Scan(&conn, &changeType); err != nil {
		t.Fatalf("expected exactly one open alert: %v", err)
	}
	if conn != "analytics" || changeType != "latency" {
		t.Fatalf("alert connection_id=%q change_type=%q, want analytics/latency", conn, changeType)
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

	// Baseline the linked query at ~200ms/call across a few intervals, then mark applied.
	fixText := "SELECT * FROM orders WHERE customer_id = 42"
	fixCum := cumCounters{}
	seedPoll(t, ctx, pool, org, "default", 45, qid, fixText, fixCum)
	for i := 0; i < 3; i++ {
		addInterval(t, ctx, pool, org, "default", &fixCum, 35-(i*10), qid, fixText, 50, 10000, 500) // 200ms/call
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

	// A later interval shows the query is ~3x faster (65ms/call) → reconcile confirms.
	addInterval(t, ctx, pool, org, "default", &fixCum, 0, qid, fixText, 50, 3250, 500)
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
