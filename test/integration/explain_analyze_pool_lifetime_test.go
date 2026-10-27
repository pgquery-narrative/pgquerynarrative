package integration

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/internal/queryrunner"
	"github.com/pgquerynarrative/pgquerynarrative/test/helpers"
)

// Bug: Explain's EXPLAIN ANALYZE branch acquires a connection explicitly
// (pool.Acquire + conn.BeginTx) instead of through pool.BeginTx, so it can
// control whether that connection is released or destroyed afterward. But
// pool.BeginTx's own Tx wrapper releases its connection back to the pool as
// part of Commit/Rollback (pgxpool.Tx.Commit calls tx.c.Release() — see
// pgxpool/tx.go) — a side effect this code loses by calling conn.BeginTx
// directly. The connection was only freed via a deferred discardConn that
// fires when Explain itself returns, which is AFTER
// Explain calls enrichExplainFindings to add catalog evidence.
// enrichExplainFindings acquires its own connections from the very same
// pool (pool.QueryRow does its own Acquire/Release per call) while the
// EXPLAIN's own connection is still checked out, so with a small pool this
// serializes unrelated work behind a connection that is already done being
// used, and with MaxConns=1 it can self-deadlock: enrichment waits for the
// one connection the function itself is still holding.
//
// This test uses a table with no index on the filtered column, so the plan
// carries a seq_scan finding enrichExplainFindings can attach IndexAdvice to
// (proving enrichment actually ran, not that it was skipped), and a
// MaxConns=1 pool to force the self-wait deterministically.
func TestExplainAnalyzeReleasesConnectionBeforeCatalogEnrichment(t *testing.T) {
	ctx := context.Background()
	container := helpers.RunPostgresContainer(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })
	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	waitReady(t, ctx, connStr)
	helpers.RunMigrations(t, connStr)

	admin, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	if _, err := admin.Exec(ctx, `
		INSERT INTO demo.sales (id, date, product_category, product_name, quantity, unit_price, total_amount, region, sales_rep)
		SELECT gen_random_uuid(), '2025-01-01', 'Electronics', 'Widget', 1, 10, 10, 'NoIndexRegion', 'A'
		FROM generate_series(1, 50)
	`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cfg, err := pgxpool.ParseConfig(connStr)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	// A short queryLimit makes "the call waited for its own deadline" and
	// "the call returned promptly" trivially distinguishable.
	const queryLimit = 8 * time.Second
	validator := queryrunner.NewValidator([]string{"demo"}, 10000)
	runner := queryrunner.NewRunner(pool, validator, 1000, queryLimit, queryrunner.WithExplainAnalyze(true))

	sql := `SELECT id FROM demo.sales WHERE region = 'NoIndexRegion'`

	start := time.Now()
	result, err := runner.Explain(ctx, sql, true)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}

	if elapsed >= queryLimit {
		t.Fatalf("Explain took %v, at or past its own %v deadline — catalog enrichment waited for a connection this call was still holding itself", elapsed, queryLimit)
	}
	t.Logf("Explain completed in %v (queryLimit %v)", elapsed, queryLimit)

	// Catalog enrichment having actually run — not silently skipped because
	// r.activePool(ctx) returned a pool that just never granted a connection
	// in time — is evidenced by IndexAdvice (built from pg_catalog index
	// definitions enrichExplainFindings queried for) being attached to at
	// least one finding. demo.sales is a partitioned table (seeded by the
	// product's migrations) with its own per-partition indexes, so the
	// finding this query produces is a cardinality/bitmap-scan one rather
	// than a plain seq scan — which category fired is incidental; the point
	// is that catalog-backed advice reached the result at all.
	var sawCatalogEvidence bool
	for _, f := range result.Findings {
		if f.IndexAdvice != nil {
			sawCatalogEvidence = true
			break
		}
	}
	if !sawCatalogEvidence {
		t.Fatalf("catalog evidence missing: enrichment did not attach IndexAdvice to any finding: %+v", result.Findings)
	}

	// The pool must still be usable immediately afterward — not just
	// eventually, once some earlier call's deferred cleanup got around to
	// firing.
	start2 := time.Now()
	if _, err := runner.Explain(ctx, sql, true); err != nil {
		t.Fatalf("second Explain after the first: %v", err)
	}
	if elapsed2 := time.Since(start2); elapsed2 >= queryLimit {
		t.Fatalf("second Explain took %v, pool did not recover promptly", elapsed2)
	}
}
