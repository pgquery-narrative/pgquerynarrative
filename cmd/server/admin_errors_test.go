package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/test/testhelpers"
)

// The admin write handlers must not hand a database error to the caller: it names constraints,
// tables and columns.
func TestAdminWriteHandlersDoNotLeakDatabaseErrors(t *testing.T) {
	ctx := context.Background()
	container := testhelpers.RunPostgresContainer(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })
	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; ; i++ {
		p, perr := pgxpool.New(ctx, connStr)
		if perr == nil {
			perr = p.Ping(ctx)
			p.Close()
		}
		if perr == nil {
			break
		}
		if i > 75 {
			t.Fatalf("postgres not ready: %v", perr)
		}
		time.Sleep(200 * time.Millisecond)
	}
	path, err := filepath.Abs("../../app/db/migrations")
	if err != nil {
		t.Fatal(err)
	}
	m, err := migrate.New("file://"+path, connStr)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	deps := adminDeps{membership: auth.NewMembershipStore(pool, false), connAuthz: auth.NewConnectionAuthorizer(pool)}
	post := func(h func(http.ResponseWriter, *http.Request, adminDeps), body string) (int, string) {
		req := httptest.NewRequest(http.MethodPost, "/x", bytes.NewBufferString(body))
		req = req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{UserID: "root", Role: auth.RolePlatformAdmin}))
		w := httptest.NewRecorder()
		h(w, req, deps)
		return w.Code, w.Body.String()
	}

	if code, _ := post(adminCreateOrganization, `{"name":"Acme","slug":"acme"}`); code != http.StatusCreated {
		t.Fatalf("first create: %d", code)
	}
	leaky := []string{"constraint", "SQLSTATE", "duplicate key", "violates", "invalid input syntax", "relation ", "app."}
	for name, tc := range map[string]struct {
		h    func(http.ResponseWriter, *http.Request, adminDeps)
		body string
		want int
	}{
		"duplicate organization slug":                    {adminCreateOrganization, `{"name":"Acme 2","slug":"acme"}`, http.StatusConflict},
		"membership in a malformed organization id":      {adminUpsertMembership, `{"organization_id":"not-a-uuid","user_id":"u","role":"viewer"}`, http.StatusBadRequest},
		"membership in an unknown organization":          {adminUpsertMembership, `{"organization_id":"00000000-0000-0000-0000-00000000dead","user_id":"u","role":"viewer"}`, http.StatusBadRequest},
		"connection assigned to an unknown organization": {adminAssignConnection, `{"organization_id":"00000000-0000-0000-0000-00000000dead","connection_id":"default"}`, http.StatusBadRequest},
	} {
		code, body := post(tc.h, tc.body)
		if code != tc.want {
			t.Errorf("%s: status %d, want %d (%s)", name, code, tc.want, strings.TrimSpace(body))
		}
		for _, w := range leaky {
			if strings.Contains(body, w) {
				t.Errorf("%s: the response exposes database detail %q: %s", name, w, strings.TrimSpace(body))
			}
		}
	}
	// A validation message the store wrote itself is still shown: it tells the caller what to fix.
	if code, body := post(adminCreateOrganization, `{"name":"","slug":""}`); code != http.StatusBadRequest || !strings.Contains(body, "required") {
		t.Errorf("a missing field must say so: %d %q", code, body)
	}
}
