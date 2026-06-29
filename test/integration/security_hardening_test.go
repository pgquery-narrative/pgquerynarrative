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

	"github.com/pgquerynarrative/pgquerynarrative/app/queryrunner"
	"github.com/pgquerynarrative/pgquerynarrative/test/testhelpers"
)

func TestReadOnlyRole_CannotQueryPublicSchema(t *testing.T) {
	ctx := context.Background()
	container := testhelpers.RunPostgresContainer(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	waitForPostgres(t, ctx, connStr)

	migrationsPath, err := filepath.Abs("../../app/db/migrations")
	if err != nil {
		t.Fatalf("migrations path: %v", err)
	}
	m, err := migrate.New("file://"+migrationsPath, connStr)
	if err != nil {
		t.Fatalf("migrator: %v", err)
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		t.Fatalf("migrate up: %v", err)
	}

	readonlyURL := strings.Replace(connStr, "postgres://postgres:", "postgres://pgquerynarrative_readonly:pgquerynarrative_readonly@", 1)
	pool, err := pgxpool.New(ctx, readonlyURL)
	if err != nil {
		t.Fatalf("readonly pool: %v", err)
	}
	defer pool.Close()

	var one int
	err = pool.QueryRow(ctx, "SELECT 1 FROM public.pg_database LIMIT 1").Scan(&one)
	if err == nil {
		t.Fatal("readonly role should not be able to query public.pg_database")
	}

	validator := queryrunner.NewValidator([]string{"demo"}, 10000)
	if err := validator.Validate("SELECT * FROM sales"); err == nil {
		t.Fatal("unqualified table should be rejected")
	}
}

func waitForPostgres(t *testing.T, ctx context.Context, connStr string) {
	t.Helper()
	waitCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for {
		pool, err := pgxpool.New(waitCtx, connStr)
		if err == nil {
			err = pool.Ping(waitCtx)
			pool.Close()
			if err == nil {
				return
			}
		}
		if waitCtx.Err() != nil {
			t.Fatalf("postgres not ready: %v", err)
		}
		time.Sleep(500 * time.Millisecond)
	}
}
