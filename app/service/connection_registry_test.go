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
