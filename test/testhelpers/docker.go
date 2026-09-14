package testhelpers

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

func RunPostgresContainer(t *testing.T, ctx context.Context) *postgres.PostgresContainer {
	t.Helper()

	if os.Getenv("DOCKER_API_VERSION") == "" {
		// Default to 1.44 so newer Docker daemons accept the client (daemon may require minimum 1.44).
		_ = os.Setenv("DOCKER_API_VERSION", "1.44")
	}

	defer func() {
		if r := recover(); r != nil {
			t.Skipf("docker not available or API mismatch: %v", r)
		}
	}()

	// Use pgvector image so migration 000007_pgvector_embeddings can run.
	container, err := postgres.Run(ctx, "pgvector/pgvector:pg18",
		postgres.WithDatabase("pgquerynarrative"),
		postgres.WithUsername("postgres"),
		postgres.WithPassword("postgres"),
	)
	if err != nil {
		t.Skipf("docker not available: %v", err)
	}
	return container
}

// RunPostgresHypopgContainer builds and starts Postgres from the project's own
// tools/docker/postgres-hypopg.Dockerfile — the same image docker-compose.yml
// uses for local dev — so tests can exercise the real hypopg planner-backed
// path (app/queryrunner/hypopg.go's projectWithHypopg) instead of only its
// labeled-heuristic fallback. Building an image is slower than pulling one, so
// callers should reserve this for tests specifically about hypopg and use
// RunPostgresContainer otherwise. Skips (not fails) the test if Docker or the
// build is unavailable, matching RunPostgresContainer's behavior — this is
// test infrastructure, not a product invariant, so an environment without a
// working Docker build must not fail the suite.
func RunPostgresHypopgContainer(t *testing.T, ctx context.Context) *postgres.PostgresContainer {
	t.Helper()

	if os.Getenv("DOCKER_API_VERSION") == "" {
		_ = os.Setenv("DOCKER_API_VERSION", "1.44")
	}

	defer func() {
		if r := recover(); r != nil {
			t.Skipf("docker not available or API mismatch: %v", r)
		}
	}()

	dockerfileDir, err := filepath.Abs("../../tools/docker")
	if err != nil {
		t.Skipf("resolve hypopg Dockerfile context: %v", err)
	}

	container, err := postgres.Run(ctx, "",
		testcontainers.WithDockerfile(testcontainers.FromDockerfile{
			Context:    dockerfileDir,
			Dockerfile: "postgres-hypopg.Dockerfile",
			KeepImage:  true, // the build is slow; let Docker cache and reuse it across test runs
		}),
		postgres.WithDatabase("pgquerynarrative"),
		postgres.WithUsername("postgres"),
		postgres.WithPassword("postgres"),
	)
	if err != nil {
		t.Skipf("hypopg postgres image not available: %v", err)
	}
	return container
}
