package helpers

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
)

// PostgRESTImage pins the real server used to verify log capture and Server-Timing.
const PostgRESTImage = "postgrest/postgrest:v16.4"

// PostgRESTStack is a disposable PostgreSQL and a PostgREST container on one
// Docker network. Logs are enabled so each request's generated SQL, bind values
// and session context reach the PostgreSQL log.
type PostgRESTStack struct {
	PG *postgres.PostgresContainer
	// ConnStr is the superuser connection string, reachable from the test process.
	ConnStr string
	// ReaderConnStr connects as pqn_reader, the read-only role the product queries use.
	ReaderConnStr string
	// BaseURL is the PostgREST base URL reachable from the test process.
	BaseURL string
}

// RunPostgRESTStack starts PostgreSQL, applies the product migrations and
// setupSQL (roles, schemas, data, grants), and only then starts PostgREST, so
// its schema cache includes setupSQL's api schema. pgArgs are extra server
// arguments, e.g. "-c", "logging_collector=on".
func RunPostgRESTStack(t *testing.T, ctx context.Context, setupSQL, jwtSecret string, pgArgs ...string) *PostgRESTStack {
	t.Helper()
	if os.Getenv("DOCKER_API_VERSION") == "" {
		_ = os.Setenv("DOCKER_API_VERSION", "1.44")
	}

	nw, err := network.New(ctx)
	if err != nil {
		t.Fatalf("PostgREST verification requires Docker: %v", err)
	}
	t.Cleanup(func() { _ = nw.Remove(context.Background()) })

	pgImage := os.Getenv("POSTGREST_TEST_POSTGRES_IMAGE")
	if pgImage == "" {
		pgImage = "pgvector/pgvector:pg18"
	}
	pg, err := postgres.Run(ctx, pgImage,
		postgres.WithDatabase("pgquerynarrative"),
		postgres.WithUsername("postgres"),
		postgres.WithPassword("postgres"),
		network.WithNetwork([]string{"postgres"}, nw),
		testcontainers.WithCmdArgs(pgArgs...),
	)
	if err != nil {
		t.Fatalf("PostgREST verification requires PostgreSQL: %v", err)
	}
	t.Cleanup(func() { _ = pg.Terminate(context.Background()) })

	connStr, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	waitForPostgres(t, ctx, connStr)
	RunMigrations(t, connStr)

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	// Statement logs carry the statement, its DETAIL parameters, and the process
	// id; compute_query_id adds a query identifier to the prefix.
	for _, stmt := range []string{
		`ALTER SYSTEM SET log_min_duration_statement = 0`,
		`ALTER SYSTEM SET log_parameter_max_length = -1`,
		`ALTER SYSTEM SET log_line_prefix = '%m [%p] %Q app=%a '`,
		`ALTER SYSTEM SET compute_query_id = 'on'`,
		`SELECT pg_reload_conf()`,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("enable statement logging (%s): %v", stmt, err)
		}
	}
	if _, err := pool.Exec(ctx, setupSQL); err != nil {
		t.Fatalf("setup SQL: %v", err)
	}

	pgrstImage := os.Getenv("POSTGREST_TEST_IMAGE")
	if pgrstImage == "" {
		pgrstImage = PostgRESTImage
	}
	pgrst, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        pgrstImage,
			ExposedPorts: []string{"3000/tcp"},
			Networks:     []string{nw.Name},
			Env: map[string]string{
				"PGRST_DB_URI":                "postgres://authenticator:auth@postgres:5432/pgquerynarrative?sslmode=disable",
				"PGRST_DB_SCHEMAS":            "api",
				"PGRST_DB_ANON_ROLE":          "web_anon",
				"PGRST_SERVER_TIMING_ENABLED": "true",
				"PGRST_JWT_SECRET":            jwtSecret,
			},
			WaitingFor: wait.ForLog("Schema cache loaded").WithStartupTimeout(time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("PostgREST container failed: %v", err)
	}
	t.Cleanup(func() { _ = pgrst.Terminate(context.Background()) })

	host, err := pgrst.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := pgrst.MappedPort(ctx, "3000/tcp")
	if err != nil {
		t.Fatal(err)
	}

	return &PostgRESTStack{
		PG:            pg,
		ConnStr:       connStr,
		ReaderConnStr: strings.Replace(connStr, "postgres:postgres@", "pqn_reader:reader@", 1),
		BaseURL:       fmt.Sprintf("http://%s:%s", host, port.Port()),
	}
}

// waitForPostgres retries until a connection succeeds. The image restarts once
// during initdb, after the container already reports ready.
func waitForPostgres(t *testing.T, ctx context.Context, connStr string) {
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

// PostgreSQLLogs returns the Postgres container's log output so far.
func (s *PostgRESTStack) PostgreSQLLogs(ctx context.Context) (string, error) {
	rc, err := s.PG.Logs(ctx)
	if err != nil {
		return "", err
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	return string(b), err
}
