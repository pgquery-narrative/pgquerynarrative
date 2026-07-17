package integration

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/app/db"
	"github.com/pgquerynarrative/pgquerynarrative/app/embedding"
	"github.com/pgquerynarrative/pgquerynarrative/test/testhelpers"
)

// mockEmbedder returns axis-aligned unit vectors so similar-text retrieval is deterministic in tests.
type mockEmbedder struct{}

func (mockEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	v := make([]float32, embedding.EmbeddingVectorDimension)
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "region"):
		v[0] = 1
	case strings.Contains(lower, "category"):
		v[1] = 1
	default:
		v[2] = 1
	}
	return v, nil
}

func TestFindSimilarQueriesPgvectorIntegration(t *testing.T) {
	ctx := context.Background()
	container := testhelpers.RunPostgresContainer(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for {
		pool, pingErr := pgxpool.New(waitCtx, connStr)
		if pingErr == nil {
			pingErr = pool.Ping(waitCtx)
			pool.Close()
			if pingErr == nil {
				break
			}
		}
		if waitCtx.Err() != nil {
			t.Fatalf("postgres not ready: %v", pingErr)
		}
		time.Sleep(500 * time.Millisecond)
	}

	migrationsPath, err := filepath.Abs("../../app/db/migrations")
	if err != nil {
		t.Fatalf("migrations path: %v", err)
	}
	m, err := migrate.New("file://"+migrationsPath, connStr)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		t.Fatalf("migrate up: %v", err)
	}

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	var hasVector bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'vector')`).Scan(&hasVector); err != nil {
		t.Fatalf("check vector: %v", err)
	}
	if !hasVector {
		t.Skip("pgvector extension not installed in test container")
	}

	_, err = pool.Exec(ctx, `
		INSERT INTO app.saved_queries (id, name, sql, connection_id, organization_id)
		VALUES
		  ('11111111-1111-1111-1111-111111111101', 'By region', 'SELECT region, SUM(total_amount) FROM demo.sales GROUP BY region', 'default', '00000000-0000-0000-0000-000000000001'),
		  ('11111111-1111-1111-1111-111111111102', 'By category', 'SELECT product_category, COUNT(*) FROM demo.sales GROUP BY product_category', 'default', '00000000-0000-0000-0000-000000000001')
		ON CONFLICT (id) DO NOTHING
	`)
	if err != nil {
		t.Fatalf("seed saved_queries: %v", err)
	}

	emb := mockEmbedder{}
	ctx = auth.WithPrincipal(ctx, auth.Principal{UserID: "test", OrgID: auth.DefaultOrgID(), Role: auth.RoleAnalyst})
	store := embedding.NewStore(db.NewOrgScoped(pool))
	for _, sq := range []struct {
		id, text string
	}{
		{"11111111-1111-1111-1111-111111111101", "rollup sales by region"},
		{"11111111-1111-1111-1111-111111111102", "count products by category"},
	} {
		vec, embedErr := emb.Embed(ctx, sq.text)
		if embedErr != nil {
			t.Fatalf("embed: %v", embedErr)
		}
		if err := store.Upsert(ctx, sq.id, vec, "mock"); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}

	queryVec, err := emb.Embed(ctx, "show me revenue by region")
	if err != nil {
		t.Fatalf("query embed: %v", err)
	}
	similar, err := store.FindSimilar(ctx, queryVec, 3)
	if err != nil {
		t.Fatalf("FindSimilar: %v", err)
	}
	if len(similar) == 0 {
		t.Fatal("expected similar queries")
	}
	if similar[0].Name != "By region" {
		t.Fatalf("top match = %q, want By region", similar[0].Name)
	}
	if similar[0].Score <= 0 {
		t.Fatalf("expected positive similarity score, got %v", similar[0].Score)
	}

	var usedPgvector bool
	if err := pool.QueryRow(ctx, `
		SELECT embedding_vector IS NOT NULL FROM app.query_embeddings
		WHERE saved_query_id = '11111111-1111-1111-1111-111111111101'::uuid
	`).Scan(&usedPgvector); err != nil {
		t.Fatalf("check embedding_vector: %v", err)
	}
	if !usedPgvector {
		t.Fatal("expected embedding_vector column populated (pgvector path)")
	}
}
