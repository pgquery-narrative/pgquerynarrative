package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/internal/queryrunner"
	"github.com/pgquerynarrative/pgquerynarrative/test/helpers"
)

// concurrentWriteTracer runs write exactly once, the instant a query whose
// SQL contains marker finishes — landing a concurrent write deterministically
// between two reads, with no sleep racing the real query timing.
type concurrentWriteTracer struct {
	marker   string
	write    func(ctx context.Context) error
	fired    bool
	writeErr error
}

type concurrentWriteTraceKey struct{}

func (tr *concurrentWriteTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, concurrentWriteTraceKey{}, strings.Contains(d.SQL, tr.marker))
}

func (tr *concurrentWriteTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	matched, _ := ctx.Value(concurrentWriteTraceKey{}).(bool)
	if !matched || tr.fired {
		return
	}
	tr.fired = true
	tr.writeErr = tr.write(context.Background())
}

// equivalenceWaitForPostgres retries a plain connection until it succeeds —
// container startup readiness, not coordination with a concurrent writer
// (see concurrentWriteTracer for that).
func equivalenceWaitForPostgres(t *testing.T, ctx context.Context, connStr string) {
	t.Helper()
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		pool, err := pgxpool.New(waitCtx, connStr)
		if err == nil {
			err = pool.Ping(waitCtx)
			pool.Close()
		}
		if err == nil {
			return
		}
		if waitCtx.Err() != nil {
			t.Fatalf("postgres not ready: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func equivalenceTracedPool(t *testing.T, ctx context.Context, connStr string, tracer pgx.QueryTracer) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(connStr)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Tracer = tracer
	p, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

// Bug: compareResultEquivalence used to run the before and after queries in
// two separate transactions (two separate Runner.Run calls), each its own
// snapshot. SELECT n and SELECT n-1 against a row moving from n=1 to n=2
// disagree for every single snapshot (1 vs 0, or 2 vs 1) — but a concurrent
// write landing between the two separate reads could make an old "before"
// value and a new "after" value coincidentally equal (1 vs 1), reporting
// VerifiedEqual for queries that can never actually agree.
//
// The fix (Runner.RunSnapshot) holds one REPEATABLE READ transaction across
// both reads, so they always see the same snapshot. This proves both
// directions: unequal queries must never pass because of a write landing
// mid-comparison, and identical queries must still agree despite one.
func TestCompareResultEquivalenceUsesOneSnapshotAcrossAConcurrentWrite(t *testing.T) {
	ctx := context.Background()
	container := helpers.RunPostgresContainer(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })
	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	equivalenceWaitForPostgres(t, ctx, connStr)

	setup, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(setup.Close)
	if _, err := setup.Exec(ctx, `CREATE SCHEMA eq; CREATE TABLE eq.live_values(n int)`); err != nil {
		t.Fatal(err)
	}

	t.Run("unequal queries cannot pass", func(t *testing.T) {
		if _, err := setup.Exec(ctx, `TRUNCATE eq.live_values; INSERT INTO eq.live_values VALUES (1)`); err != nil {
			t.Fatal(err)
		}
		tracer := &concurrentWriteTracer{marker: "AS pgqn_s", write: func(ctx context.Context) error {
			_, err := setup.Exec(ctx, `UPDATE eq.live_values SET n = 2`)
			return err
		}}
		readPool := equivalenceTracedPool(t, ctx, connStr, tracer)
		runner := queryrunner.NewRunner(readPool, queryrunner.NewValidator([]string{"eq"}, 10000), 1000, 5*time.Second)

		result := compareResultEquivalence(ctx, runner, `SELECT n FROM eq.live_values`, `SELECT n-1 FROM eq.live_values`)
		if !tracer.fired {
			t.Fatal("tracer never fired; test did not exercise the concurrent-write window")
		}
		if tracer.writeErr != nil {
			t.Fatalf("concurrent write failed: %v", tracer.writeErr)
		}
		t.Logf("comparison: %+v", result)
		if result.Status == EquivalenceVerifiedEqual {
			t.Fatal("VerifiedEqual for n vs n-1 across a concurrent write: queries disagree for every snapshot")
		}
	})

	t.Run("identical queries remain equal during writes", func(t *testing.T) {
		if _, err := setup.Exec(ctx, `TRUNCATE eq.live_values; INSERT INTO eq.live_values VALUES (1)`); err != nil {
			t.Fatal(err)
		}
		tracer := &concurrentWriteTracer{marker: "AS pgqn_s", write: func(ctx context.Context) error {
			_, err := setup.Exec(ctx, `UPDATE eq.live_values SET n = 99`)
			return err
		}}
		readPool := equivalenceTracedPool(t, ctx, connStr, tracer)
		runner := queryrunner.NewRunner(readPool, queryrunner.NewValidator([]string{"eq"}, 10000), 1000, 5*time.Second)

		result := compareResultEquivalence(ctx, runner, `SELECT n FROM eq.live_values`, `SELECT n FROM eq.live_values`)
		if !tracer.fired {
			t.Fatal("tracer never fired; test did not exercise the concurrent-write window")
		}
		if tracer.writeErr != nil {
			t.Fatalf("concurrent write failed: %v", tracer.writeErr)
		}
		t.Logf("comparison: %+v", result)
		if result.Status != EquivalenceVerifiedEqual {
			t.Fatalf("identical queries must remain VerifiedEqual despite a concurrent write landing mid-comparison, got %+v", result)
		}
	})
}

// Bug: the fallback path (compareByCountAndSample) claimed FullCompare/
// VerifiedEqual whenever beforeCount <= equivalenceSampleCap, without
// checking that the sample it actually fetched held that many rows. A
// runner configured with a lower row cap (maxRows) than the sample needs
// silently truncates every sample query's own LIMIT down to maxRows — so two
// genuinely different 2-row results that merely share the same first row
// both got sampled down to that one shared row, fingerprinted equal, and
// were reported as a fully verified match.
//
// maxColumns=2 forces this test down the fallback path at all: the primary
// fingerprint query projects 5 aggregate columns and fails that cap, exactly
// as it would for any runner configured tighter than this comparison's own
// internal queries — a normal, supported configuration, not a contrived
// edge case.
func TestCompareResultEquivalenceFallbackDoesNotClaimFullCompareWhenTruncated(t *testing.T) {
	ctx := context.Background()
	container := helpers.RunPostgresContainer(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })
	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	equivalenceWaitForPostgres(t, ctx, connStr)
	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	runner := queryrunner.NewRunner(pool, queryrunner.NewValidator(nil, 10000), 1, 5*time.Second, queryrunner.WithResultLimits(100000, 100000, 2))

	result := compareResultEquivalence(ctx, runner,
		`SELECT n FROM (VALUES(1),(2)) AS t(n) ORDER BY n`,
		`SELECT n FROM (VALUES(1),(3)) AS t(n) ORDER BY n`)
	t.Logf("comparison: %+v", result)
	if result.Status == EquivalenceVerifiedEqual {
		t.Fatal("VerifiedEqual after comparing only 1 of 2 rows, with differing unexamined rows")
	}
	if result.FullCompare {
		t.Fatalf("FullCompare must not be true when the runner's row limit truncated the sample below the full result: %+v", result)
	}

	// Positive control: with the same tight runner limits, a comparison whose
	// true row count fits inside maxRows is not truncated and must still be
	// reported as a genuine full compare.
	full := compareResultEquivalence(ctx, runner,
		`SELECT n FROM (VALUES(1)) AS t(n)`,
		`SELECT n FROM (VALUES(1)) AS t(n)`)
	t.Logf("comparison (control): %+v", full)
	if full.Status != EquivalenceVerifiedEqual || !full.FullCompare {
		t.Fatalf("a genuinely untruncated 1-row match must still report VerifiedEqual/FullCompare, got %+v", full)
	}
}

// compareByCountAndSample must report a BeforeCount that actually matches
// how many rows it sampled, not only avoid false VerifiedEqual between two
// disagreeing queries (TestCompareResultEquivalenceUsesOneSnapshotAcrossAConcurrentWrite
// covers that, for the primary compareByFingerprint path). An earlier version
// ran COUNT(*) and the sample fetch as two separate RunSnapshot calls — two
// separate transactions, each its own snapshot. A write landing between them
// that changes the row count (here, an INSERT) left COUNT(*) and the sample
// looking at two different states: COUNT(*) correctly reported the
// pre-insert row count, but the sample transaction, started after the
// insert committed, fetched the post-insert rows — so FullCompare claimed
// "every row matched" for a COUNT(*) that did not actually describe how
// many rows were compared. One shared snapshot for all four reads means the
// transaction's snapshot is fixed before the write can land at all (REPEATABLE
// READ fixes it at that transaction's first statement), so the insert is
// consistently invisible throughout — COUNT(*) and the sample always
// describe the same state. maxColumns=2 forces the fallback path by breaking
// the primary fingerprint query's 5 aggregate columns.
func TestCompareResultEquivalenceFallbackCountMatchesWhatItActuallySampled(t *testing.T) {
	ctx := context.Background()
	container := helpers.RunPostgresContainer(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })
	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	equivalenceWaitForPostgres(t, ctx, connStr)

	setup, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(setup.Close)
	if _, err := setup.Exec(ctx, `CREATE SCHEMA eqfb; CREATE TABLE eqfb.t(v int); INSERT INTO eqfb.t VALUES (1), (2)`); err != nil {
		t.Fatal(err)
	}

	// Fires once, when the first COUNT(*) query (wrapCountSQL's "pgqn_eq_n"
	// marker) finishes — i.e. after whatever transaction holds the count's
	// snapshot has already started, so a correct single-snapshot
	// implementation has already fixed its view of the data by the time this
	// write lands.
	tracer := &concurrentWriteTracer{marker: "pgqn_eq_n", write: func(ctx context.Context) error {
		_, err := setup.Exec(ctx, `INSERT INTO eqfb.t VALUES (3)`)
		return err
	}}
	readPool := equivalenceTracedPool(t, ctx, connStr, tracer)
	runner := queryrunner.NewRunner(readPool, queryrunner.NewValidator([]string{"eqfb"}, 10000), 1000, 5*time.Second, queryrunner.WithResultLimits(100000, 100000, 2))

	// Identical queries on both sides: this test is not about before/after
	// disagreeing, it is about COUNT(*) and the sample staying consistent
	// with each other under a concurrent write.
	result := compareResultEquivalence(ctx, runner, `SELECT v FROM eqfb.t`, `SELECT v FROM eqfb.t`)
	if !tracer.fired {
		t.Fatal("tracer never fired; test did not exercise the concurrent-write window")
	}
	if tracer.writeErr != nil {
		t.Fatalf("concurrent write failed: %v", tracer.writeErr)
	}
	t.Logf("comparison: %+v", result)
	if !result.CountsComputed {
		t.Fatalf("expected counts to be computed: %+v", result)
	}
	if result.FullCompare && int64(result.SampleRows) != result.BeforeCount {
		t.Fatalf("FullCompare claims COUNT(*)=%d fully described the comparison, but %d rows were actually sampled — COUNT(*) and the sample fetch saw different database states", result.BeforeCount, result.SampleRows)
	}
}
