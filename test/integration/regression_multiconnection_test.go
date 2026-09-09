package integration

import (
	"context"
	"testing"

	"github.com/pgquerynarrative/pgquerynarrative/api/gen/investigations"
	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/app/db"
	"github.com/pgquerynarrative/pgquerynarrative/app/service"
)

// A queryid is only unique *within* a connection: the same statement on two
// databases hashes to the same queryid. Every step of the fix lifecycle must
// therefore key on (organization, connection, queryid).
//
// Before this was enforced, the apply-time baseline and the post-deploy
// re-measurement both partitioned by queryid alone, so two connections'
// independent cumulative pg_stat_statements counters were interleaved into one
// lag() series — which could confirm a fix on connection A using connection B's
// traffic, or mark a genuinely improved query as regressed.
func TestFixLifecycle_ConnectionScopedMeasurement(t *testing.T) {
	ctx := context.Background()
	pool := regressionTestPool(t, ctx)
	org := auth.DefaultOrganizationID
	appDB := db.NewOrgScoped(pool)
	invSvc := service.NewInvestigationsService(appDB, nil, nil)
	reqCtx := auth.WithPrincipal(ctx, auth.Principal{UserID: "multi-conn", OrgID: org, Role: auth.RoleAdmin})

	// The SAME queryid on two different connections.
	const qid = "100"
	const sqlText = "SELECT * FROM orders WHERE customer_id = $1"

	// Investigation + alert live on connection A.
	var invID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.investigations (organization_id, title, sql, query_fingerprint, status, connection_id)
		VALUES ($1, 'Slow orders (A)', $2, 'fp-multi', 'complete', 'conn-a')
		RETURNING id::text
	`, org, sqlText).Scan(&invID); err != nil {
		t.Fatalf("seed investigation: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.regression_alerts (
			organization_id, title, query_text, queryid, change_type,
			change_summary, impact, source, investigation_id, connection_id)
		VALUES ($1, 'Slow orders (A)', $2, $3, 'latency', '+200%', 'high', 'poller', $4, 'conn-a')
	`, org, sqlText, qid, invID); err != nil {
		t.Fatalf("seed alert: %v", err)
	}

	// Connection A: three baseline intervals at ~200ms/call.
	aCum := cumCounters{}
	seedPoll(t, ctx, pool, org, "conn-a", 60, qid, sqlText, aCum)
	for i := 0; i < 3; i++ {
		addInterval(t, ctx, pool, org, "conn-a", &aCum, 50-(i*10), qid, sqlText, 50, 10000, 500)
	}

	// Connection B: same queryid, wildly different and much slower traffic
	// (~1000ms/call), interleaved in time with A's polls. If any step keys on
	// queryid alone, B's counters corrupt A's deltas.
	bCum := cumCounters{}
	seedPoll(t, ctx, pool, org, "conn-b", 55, qid, sqlText, bCum)
	for i := 0; i < 3; i++ {
		addInterval(t, ctx, pool, org, "conn-b", &bCum, 45-(i*10), qid, sqlText, 50, 50000, 500)
	}

	// Marking the fix applied must snapshot connection A's ~200ms baseline,
	// never a value mixed with connection B's ~1000ms series.
	upd, err := invSvc.UpdateFix(reqCtx, &investigations.UpdateFixPayload{
		ID: invID, FixStatus: strp("applied"), FixReference: strp("https://github.com/x/y/pull/1"),
	})
	if err != nil {
		t.Fatalf("UpdateFix applied: %v", err)
	}
	if upd.FixBaselineMeanMs == nil {
		t.Fatal("fix baseline was not snapshotted")
	}
	if *upd.FixBaselineMeanMs < 150 || *upd.FixBaselineMeanMs > 250 {
		t.Fatalf("apply-time baseline must come from connection A (~200ms), got %v — "+
			"a value near 1000ms means connection B's counters leaked into the window",
			*upd.FixBaselineMeanMs)
	}

	// Connection A improves sharply (~50ms/call): the fix worked.
	addInterval(t, ctx, pool, org, "conn-a", &aCum, 5, qid, sqlText, 50, 2500, 500)
	// Connection B gets *worse* at the same time (~1600ms/call). It must have no
	// influence on connection A's investigation.
	addInterval(t, ctx, pool, org, "conn-b", &bCum, 1, qid, sqlText, 50, 80000, 500)

	poller := newTestPoller(pool)
	if err := poller.EvaluateLatestPoll(ctx, org); err != nil {
		t.Fatalf("EvaluateLatestPoll: %v", err)
	}

	got, err := invSvc.Get(reqCtx, &investigations.GetPayload{ID: invID})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.FixStatus == nil || *got.FixStatus != "confirmed" {
		t.Fatalf("connection A improved 200ms -> 50ms, so its fix must be confirmed; got %v. "+
			"Connection B regressing must not affect it.", got.FixStatus)
	}
	if got.FixConfirmedMeanMs == nil || *got.FixConfirmedMeanMs > 100 {
		t.Fatalf("confirmed mean must be connection A's (~50ms), got %v", got.FixConfirmedMeanMs)
	}
}

// The mirror case: a fix on the connection that did NOT improve must not be
// confirmed just because a different connection reporting the same queryid did.
func TestFixLifecycle_OtherConnectionImprovementDoesNotConfirm(t *testing.T) {
	ctx := context.Background()
	pool := regressionTestPool(t, ctx)
	org := auth.DefaultOrganizationID
	appDB := db.NewOrgScoped(pool)
	invSvc := service.NewInvestigationsService(appDB, nil, nil)
	reqCtx := auth.WithPrincipal(ctx, auth.Principal{UserID: "multi-conn-2", OrgID: org, Role: auth.RoleAdmin})

	const qid = "200"
	const sqlText = "SELECT * FROM invoices WHERE account_id = $1"

	// The investigation is on connection B, the one that stays slow.
	var invID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.investigations (organization_id, title, sql, query_fingerprint, status, connection_id)
		VALUES ($1, 'Slow invoices (B)', $2, 'fp-multi-2', 'complete', 'conn-b')
		RETURNING id::text
	`, org, sqlText).Scan(&invID); err != nil {
		t.Fatalf("seed investigation: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO app.regression_alerts (
			organization_id, title, query_text, queryid, change_type,
			change_summary, impact, source, investigation_id, connection_id)
		VALUES ($1, 'Slow invoices (B)', $2, $3, 'latency', '+200%', 'high', 'poller', $4, 'conn-b')
	`, org, sqlText, qid, invID); err != nil {
		t.Fatalf("seed alert: %v", err)
	}

	// Both connections baseline at ~200ms/call.
	aCum, bCum := cumCounters{}, cumCounters{}
	seedPoll(t, ctx, pool, org, "conn-a", 60, qid, sqlText, aCum)
	seedPoll(t, ctx, pool, org, "conn-b", 59, qid, sqlText, bCum)
	for i := 0; i < 3; i++ {
		addInterval(t, ctx, pool, org, "conn-a", &aCum, 50-(i*10), qid, sqlText, 50, 10000, 500)
		addInterval(t, ctx, pool, org, "conn-b", &bCum, 49-(i*10), qid, sqlText, 50, 10000, 500)
	}

	if _, err := invSvc.UpdateFix(reqCtx, &investigations.UpdateFixPayload{
		ID: invID, FixStatus: strp("applied"),
	}); err != nil {
		t.Fatalf("UpdateFix applied: %v", err)
	}

	// Connection A improves dramatically; connection B (the fix's own connection)
	// does not move at all.
	addInterval(t, ctx, pool, org, "conn-a", &aCum, 5, qid, sqlText, 50, 1000, 500)  // 20ms/call
	addInterval(t, ctx, pool, org, "conn-b", &bCum, 1, qid, sqlText, 50, 10000, 500) // still 200ms/call

	poller := newTestPoller(pool)
	if err := poller.EvaluateLatestPoll(ctx, org); err != nil {
		t.Fatalf("EvaluateLatestPoll: %v", err)
	}

	got, err := invSvc.Get(reqCtx, &investigations.GetPayload{ID: invID})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.FixStatus != nil && *got.FixStatus == "confirmed" {
		t.Fatal("connection B did not improve, so its fix must NOT be confirmed — " +
			"connection A's improvement leaked across the queryid")
	}
}
