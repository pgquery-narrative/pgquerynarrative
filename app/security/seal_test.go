package security

import "testing"

func TestSealOpenRoundTrip(t *testing.T) {
	secret := []byte("test-data-encryption-key-32b!!")
	sealed, err := Seal(secret, `{"Filter":"(email = 'a@b.c')"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !IsSealed(sealed) {
		t.Fatalf("expected sealed envelope, got %q", sealed)
	}
	plain, err := Open(secret, sealed)
	if err != nil {
		t.Fatal(err)
	}
	if plain != `{"Filter":"(email = 'a@b.c')"}` {
		t.Fatalf("round-trip mismatch: %q", plain)
	}
}

func TestSealRequiresSecret(t *testing.T) {
	if _, err := Seal(nil, "x"); err == nil {
		t.Fatal("expected error")
	}
}
