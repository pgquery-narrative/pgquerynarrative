package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/app/catalog"
	apperrors "github.com/pgquerynarrative/pgquerynarrative/app/errors"
)

func TestLoaderFor(t *testing.T) {
	primary := &catalog.Loader{}
	r := newConnectionResolver("primary", nil, map[string]*catalog.Loader{"primary": primary}, nil)

	t.Run("a nil connection id resolves to the default", func(t *testing.T) {
		got, err := r.loaderFor(nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != primary {
			t.Error("expected the default connection's loader")
		}
	})

	t.Run("an explicit known id resolves to that loader", func(t *testing.T) {
		id := "primary"
		got, err := r.loaderFor(&id)
		if err != nil || got != primary {
			t.Fatalf("got %v / %v", got, err)
		}
	})

	t.Run("an unknown id is not silently redirected to the default", func(t *testing.T) {
		id := "does-not-exist"
		if _, err := r.loaderFor(&id); !errors.Is(err, apperrors.ErrConnectionNotFound) {
			t.Errorf("expected ErrConnectionNotFound, got %v", err)
		}
	})

	t.Run("a default id with no registered loader is an error, not a nil loader", func(t *testing.T) {
		empty := newConnectionResolver("primary", nil, map[string]*catalog.Loader{}, nil)
		if _, err := empty.loaderFor(nil); !errors.Is(err, apperrors.ErrConnectionNotFound) {
			t.Errorf("expected ErrConnectionNotFound, got %v", err)
		}
	})
}

func TestReadOnlyUserFor(t *testing.T) {
	r := newConnectionResolver("primary", nil,
		map[string]*catalog.Loader{"primary": {}, "replica": {}},
		map[string]string{"primary": "  analytics_ro  ", "replica": "   "},
	)

	t.Run("a configured user is returned trimmed", func(t *testing.T) {
		got, err := r.readOnlyUserFor(nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "analytics_ro" {
			t.Errorf("got %q, want %q", got, "analytics_ro")
		}
	})

	t.Run("a blank configured user falls back to the product default", func(t *testing.T) {
		id := "replica"
		got, err := r.readOnlyUserFor(&id)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "pgquerynarrative_readonly" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("an unconfigured connection uses the product default", func(t *testing.T) {
		bare := newConnectionResolver("primary", nil, map[string]*catalog.Loader{"primary": {}}, nil)
		got, err := bare.readOnlyUserFor(nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "pgquerynarrative_readonly" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("an unknown connection is an error, not the default user", func(t *testing.T) {
		id := "nope"
		if _, err := r.readOnlyUserFor(&id); !errors.Is(err, apperrors.ErrConnectionNotFound) {
			t.Errorf("expected ErrConnectionNotFound, got %v", err)
		}
	})
}

func TestOrgScopeHelpers(t *testing.T) {
	t.Run("orgID reads the request organization", func(t *testing.T) {
		ctx := auth.WithPrincipal(context.Background(), auth.Principal{
			UserID: "u1", OrgID: "org-42", Role: auth.RoleAnalyst,
		})
		if got := orgID(ctx); got != "org-42" {
			t.Errorf("got %q, want org-42", got)
		}
	})

	t.Run("a context with no organization fails closed", func(t *testing.T) {
		// An empty org ID matches no RLS-scoped row, which is the safe outcome.
		if got := orgID(context.Background()); got != "" {
			t.Errorf("expected an empty org id, got %q", got)
		}
	})

	t.Run("orgNotFound does not disclose that the resource exists elsewhere", func(t *testing.T) {
		err := orgNotFound()
		if err == nil {
			t.Fatal("expected an error")
		}
		msg := strings.ToLower(err.Error())
		for _, leak := range []string{"organization", "forbidden", "permission", "denied"} {
			if strings.Contains(msg, leak) {
				t.Errorf("cross-org probing hint %q leaked in %q", leak, err.Error())
			}
		}
		if !strings.Contains(msg, "not found") {
			t.Errorf("expected a not-found message, got %q", err.Error())
		}
	})
}

func TestSanitizeClientAndStoredErrors(t *testing.T) {
	t.Run("a nil error still yields a message", func(t *testing.T) {
		if got := SanitizeClientMessage(nil); got == "" {
			t.Error("expected a non-empty fallback message")
		}
		if got := SanitizeStoredError(nil); got != "" {
			t.Errorf("a nil error stores nothing, got %q", got)
		}
	})

	t.Run("a timeout is reported as a timeout", func(t *testing.T) {
		got := SanitizeClientMessage(apperrors.ErrQueryTimeout)
		if !strings.Contains(strings.ToLower(got), "timed out") {
			t.Errorf("got %q", got)
		}
	})

	t.Run("driver detail never reaches the client or the stored record", func(t *testing.T) {
		driverErr := errors.New(`pq: relation "app.secret_ledger" does not exist (SQLSTATE 42P01)`)
		for name, got := range map[string]string{
			"client": SanitizeClientMessage(driverErr),
			"stored": SanitizeStoredError(driverErr),
		} {
			if strings.Contains(got, "secret_ledger") || strings.Contains(got, "SQLSTATE") {
				t.Errorf("%s message leaked driver detail: %q", name, got)
			}
			if got == "" {
				t.Errorf("%s message should not be empty", name)
			}
		}
	})
}
