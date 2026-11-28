package integration

import (
	"context"
	"testing"
	"time"

	"github.com/pgquerynarrative/pgquerynarrative/internal/queryrunner"
)

func TestCastRewriteResolvesReplaySearchPath(t *testing.T) {
	ctx := context.Background()
	pool, _ := pilotPostgres(t, ctx)
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `
		CREATE SCHEMA replay_types;
		CREATE DOMAIN public.citext AS numeric;
		CREATE DOMAIN replay_types.citext AS integer;
		CREATE TABLE public.cast_replay (id integer, amount public.citext);
		INSERT INTO public.cast_replay VALUES (1, 1.4), (2, 1);
	`); err != nil {
		t.Fatal(err)
	}
	runner := queryrunner.NewRunner(pool, queryrunner.NewValidator([]string{"public", "replay_types"}, 10000), 100, 10*time.Second)
	replay := queryrunner.WithReplayContext(ctx, &queryrunner.ReplayContext{
		SearchPath: []string{"replay_types", "public"},
		Method:     "GET",
		Path:       "/cast_replay",
	})
	const sql = `SELECT id FROM public.cast_replay WHERE amount::citext = '1' ORDER BY id`
	if same, ok := runner.ResolveColumnType(ctx, "public.cast_replay", "amount", "citext"); !ok || !same {
		t.Fatalf("default search_path should resolve the column's own domain: same=%v ok=%v", same, ok)
	}
	before, err := runner.Run(replay, sql, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Rows) != 2 {
		t.Fatalf("replay fixture returned %d rows, want 2", len(before.Rows))
	}
	if same, ok := runner.ResolveColumnType(replay, "public.cast_replay", "amount", "citext"); !ok || same {
		t.Fatalf("replay search_path must resolve a different domain: same=%v ok=%v", same, ok)
	}
	for _, cand := range queryrunner.SuggestRewritesForInvestigationWithTypes(replay, sql, nil, false, runner.ResolveColumnType) {
		if cand.Category != "implicit_cast" {
			continue
		}
		after, err := runner.Run(replay, cand.SQL, 10)
		if err != nil {
			t.Fatal(err)
		}
		t.Fatalf("cast resolved against the pool's default search_path instead of the replay: source rows=%v, candidate=%s rows=%v", before.Rows, cand.SQL, after.Rows)
	}
	if got := queryrunner.SuggestRewritesForInvestigationWithTypes(replay, sql, nil, true, runner.ResolveColumnType); len(got) != 0 {
		t.Fatal("imported PostgREST statements must still decline every rewrite")
	}
	if same, ok := runner.ResolveColumnType(replay, "public.cast_replay", "amount", "public.citext"); !ok || !same {
		t.Fatalf("explicit schema must still resolve the same domain: same=%v ok=%v", same, ok)
	}
	invalidRole := queryrunner.WithReplayContext(ctx, &queryrunner.ReplayContext{Role: "missing_replay_role"})
	if same, ok := runner.ResolveColumnType(invalidRole, "public.cast_replay", "amount", "citext"); ok || same {
		t.Fatal("failed replay must decline rather than fall back to the default context")
	}
	var method *string
	if err := pool.QueryRow(ctx, "SELECT current_setting('request.method', true)").Scan(&method); err != nil {
		t.Fatal(err)
	}
	if method != nil {
		t.Fatalf("catalog replay left session state in the pool: %q", *method)
	}
	if same, ok := runner.ResolveColumnType(ctx, "public.cast_replay", "amount", "citext"); !ok || !same {
		t.Fatalf("catalog replay changed the pool's default search_path: same=%v ok=%v", same, ok)
	}
}
