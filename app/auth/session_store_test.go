package auth

import (
	"testing"
)

func TestNewSessionStore_NilPool(t *testing.T) {
	if NewSessionStore(nil) != nil {
		t.Fatal("expected nil store for nil pool")
	}
}

func TestSessionStore_Enabled(t *testing.T) {
	var s SessionStore
	if s.Enabled() {
		t.Fatal("zero-value store must be disabled")
	}
	if (&SessionStore{}).Enabled() {
		t.Fatal("store without pool must be disabled")
	}
}

func TestSessionStore_DisabledOperations(t *testing.T) {
	var s SessionStore
	if _, err := s.Create(nil, Session{}, ""); err == nil {
		t.Fatal("expected error when store disabled")
	}
	if err := s.Update(nil, "id", Session{}, ""); err == nil {
		t.Fatal("expected error when store disabled")
	}
	if _, err := s.Load(nil, "id"); err == nil {
		t.Fatal("expected error when store disabled")
	}
}

func TestRandomSessionID(t *testing.T) {
	id, err := RandomSessionID()
	if err != nil {
		t.Fatalf("RandomSessionID: %v", err)
	}
	if len(id) != 32 {
		t.Fatalf("expected 32-char hex id, got len %d", len(id))
	}
	id2, err := RandomSessionID()
	if err != nil {
		t.Fatalf("RandomSessionID: %v", err)
	}
	if id == id2 {
		t.Fatal("expected unique session ids")
	}
}
