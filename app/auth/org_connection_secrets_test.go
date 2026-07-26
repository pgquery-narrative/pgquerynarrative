package auth

import (
	"testing"

	"github.com/pgquerynarrative/pgquerynarrative/app/security"
)

func TestOrgConnectionSecretSealRoundTrip(t *testing.T) {
	key := []byte("unit-test-encryption-key-32bytes!!")
	plain := "postgres://ro:secret@db.example:5432/analytics?sslmode=require"
	sealed, err := security.Seal(key, plain)
	if err != nil {
		t.Fatal(err)
	}
	if sealed == plain {
		t.Fatal("expected sealed envelope, got plaintext")
	}
	out, err := security.Open(key, sealed)
	if err != nil {
		t.Fatal(err)
	}
	if out != plain {
		t.Fatalf("round-trip mismatch: %q", out)
	}
}

func TestNewOrgConnectionSecretStoreNilPool(t *testing.T) {
	if NewOrgConnectionSecretStore(nil, "key") != nil {
		t.Fatal("expected nil store without pool")
	}
}
