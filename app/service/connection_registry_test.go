package service

import (
	"errors"
	"testing"

	apperrors "github.com/pgquerynarrative/pgquerynarrative/app/errors"
	"github.com/pgquerynarrative/pgquerynarrative/app/queryrunner"
)

func TestResolveConnectionID_RejectsUnknown(t *testing.T) {
	r := newConnectionResolver("default", map[string]*queryrunner.Runner{
		"default":   {},
		"analytics": {},
	}, nil, nil)

	unknown := "typo-connection"
	_, err := r.resolveConnectionID(&unknown)
	if !errors.Is(err, apperrors.ErrConnectionNotFound) {
		t.Fatalf("expected ErrConnectionNotFound, got %v", err)
	}

	blank := "  "
	id, err := r.resolveConnectionID(&blank)
	if err != nil {
		t.Fatalf("blank should use default: %v", err)
	}
	if id != "default" {
		t.Fatalf("got %q want default", id)
	}

	known := "analytics"
	id, err = r.resolveConnectionID(&known)
	if err != nil || id != "analytics" {
		t.Fatalf("known id: id=%q err=%v", id, err)
	}

	id, err = r.resolveConnectionID(nil)
	if err != nil || id != "default" {
		t.Fatalf("nil id: id=%q err=%v", id, err)
	}
}

func TestConnectionIDs(t *testing.T) {
	r := newConnectionResolver("default", map[string]*queryrunner.Runner{
		"analytics": {}, "default": {}, "reporting": {},
	}, nil, nil)
	got := r.connectionIDs()
	want := []string{"analytics", "default", "reporting"} // sorted
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v (sorted)", got, want)
		}
	}

	// No registry wired: fall back to the default id.
	empty := newConnectionResolver("default", nil, nil, nil)
	if ids := empty.connectionIDs(); len(ids) != 1 || ids[0] != "default" {
		t.Fatalf("empty registry should yield [default], got %v", ids)
	}
}

func TestRunnerFor_RejectsUnknown(t *testing.T) {
	r := newConnectionResolver("default", map[string]*queryrunner.Runner{
		"default": {},
	}, nil, nil)
	unknown := "missing"
	_, err := r.runnerFor(&unknown)
	if !errors.Is(err, apperrors.ErrConnectionNotFound) {
		t.Fatalf("expected ErrConnectionNotFound, got %v", err)
	}
}
