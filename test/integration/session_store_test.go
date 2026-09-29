package integration

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/internal/auth"
	"github.com/pgquerynarrative/pgquerynarrative/test/helpers"
)

// TestSessionStore_RevokeActuallyInvalidates runs as the real pgquerynarrative_app role (not the
// container's postgres superuser every other integration test uses) to guard against the class
// of bug migration 000061 fixed: app.revoke_browser_session(uuid) was REVOKE ALL FROM PUBLIC with
// no matching GRANT EXECUTE, so SessionManager.RevokeCurrent's call to it silently failed
// (best-effort, error discarded) and logout never actually invalidated the session server-side.
func TestSessionStore_RevokeActuallyInvalidates(t *testing.T) {
	ctx := context.Background()
	container := helpers.RunPostgresContainer(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	superConnStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for {
		pool, pingErr := pgxpool.New(waitCtx, superConnStr)
		if pingErr == nil {
			pingErr = pool.Ping(waitCtx)
			pool.Close()
			if pingErr == nil {
				break
			}
		}
		if waitCtx.Err() != nil {
			t.Fatal("postgres not ready")
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Create the pgquerynarrative_app role and its grants the same way a real deployment does,
	// so this test exercises the actual production permission surface, not the container
	// superuser every other integration test runs as (which would never see a missing GRANT).
	initSQL, err := os.ReadFile("../../postgres/init/00-init.sql")
	if err != nil {
		t.Fatalf("read init.sql: %v", err)
	}
	superPool, err := pgxpool.New(ctx, superConnStr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := superPool.Exec(ctx, string(initSQL)); err != nil {
		t.Fatalf("run init.sql: %v", err)
	}
	superPool.Close()

	helpers.RunMigrations(t, superConnStr)

	host, err := container.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := container.MappedPort(ctx, "5432/tcp")
	if err != nil {
		t.Fatal(err)
	}
	appConnStr := fmt.Sprintf("postgres://pgquerynarrative_app:pgquerynarrative_app@%s:%s/pgquerynarrative?sslmode=disable", host, port.Port())

	appPool, err := pgxpool.New(ctx, appConnStr)
	if err != nil {
		t.Fatalf("connect as pgquerynarrative_app: %v", err)
	}
	defer appPool.Close()

	store := auth.NewSessionStore(appPool)
	if store == nil || !store.Enabled() {
		t.Fatal("NewSessionStore did not produce an enabled store")
	}

	sess := auth.Session{UserID: "alice", OrgID: auth.DefaultOrganizationID, Role: "admin", ExpiresAt: time.Now().Add(time.Hour)}
	id, err := store.Create(ctx, sess, "")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := store.Load(ctx, id); err != nil {
		t.Fatalf("session should be readable before revoke: %v", err)
	}

	if err := store.Revoke(ctx, id); err != nil {
		t.Fatalf("revoke as pgquerynarrative_app (this is exactly the call RevokeCurrent makes on every logout): %v", err)
	}

	if _, err := store.Load(ctx, id); err == nil {
		t.Fatal("replaying a revoked session id should fail, but it succeeded")
	}
}
