package service

import (
	"strings"
	"testing"
)

func TestSealOpenProductSQL_roundTrip(t *testing.T) {
	key := []byte("test-data-encryption-key")
	plain := "SELECT 1 FROM demo.orders"
	sealed := sealProductSQL(key, plain)
	if sealed == plain || !strings.HasPrefix(sealed, "v1:") {
		t.Fatalf("expected sealed envelope, got %q", sealed)
	}
	got := openProductSQL(key, sealed)
	if got != plain {
		t.Fatalf("open = %q, want %q", got, plain)
	}
}

func TestOpenProductSQL_legacyPlaintext(t *testing.T) {
	plain := "SELECT count(*) FROM demo.users"
	if got := openProductSQL([]byte("key"), plain); got != plain {
		t.Fatalf("legacy plaintext should pass through, got %q", got)
	}
	if got := openProductSQL(nil, plain); got != plain {
		t.Fatalf("nil key with plaintext should pass through, got %q", got)
	}
}

func TestOpenProductSQL_failClosedWithoutKey(t *testing.T) {
	sealed := sealProductSQL([]byte("secret"), "SELECT 1")
	if got := openProductSQL(nil, sealed); got != "" {
		t.Fatalf("sealed without key should return empty, got %q", got)
	}
	if got := openProductSQL([]byte("wrong-key"), sealed); got != "" {
		t.Fatalf("wrong key should return empty, got %q", got)
	}
}

func TestSealProductSQL_noKeyOrEmpty(t *testing.T) {
	if got := sealProductSQL(nil, "SELECT 1"); got != "SELECT 1" {
		t.Fatalf("no key should leave plaintext, got %q", got)
	}
	if got := sealProductSQL([]byte("k"), ""); got != "" {
		t.Fatalf("empty sql should stay empty, got %q", got)
	}
}
