package service

import (
	"github.com/pgquerynarrative/pgquerynarrative/app/security"
)

// sealProductSQL encrypts SQL for at-rest storage when a data encryption key is configured.
// On seal failure the plaintext is returned so writes are not blocked by crypto errors;
// callers that require fail-closed behavior should check IsSealed after write.
func sealProductSQL(key []byte, sql string) string {
	if len(key) == 0 || sql == "" || security.IsSealed(sql) {
		return sql
	}
	sealed, err := security.Seal(key, sql)
	if err != nil {
		return sql
	}
	return sealed
}

// openProductSQL decrypts a Seal envelope when present. Legacy plaintext passes through.
// When the value is sealed but no key is available (or Open fails), returns empty string
// so sealed SQL is never returned as ciphertext to API clients.
func openProductSQL(key []byte, stored string) string {
	if stored == "" || !security.IsSealed(stored) {
		return stored
	}
	if len(key) == 0 {
		return ""
	}
	plain, err := security.Open(key, stored)
	if err != nil {
		return ""
	}
	return plain
}
