package service

import (
	"fmt"

	"github.com/pgquerynarrative/pgquerynarrative/app/security"
)

// sealProductSQL encrypts SQL for at-rest storage when a data encryption key is configured.
// When a key is configured, seal failures fail closed (error) so plaintext is never persisted.
// When no key is configured, plaintext is returned unchanged (StrictMode requires a key).
func sealProductSQL(key []byte, sql string) (string, error) {
	if sql == "" || security.IsSealed(sql) {
		return sql, nil
	}
	if len(key) == 0 {
		return sql, nil
	}
	sealed, err := security.Seal(key, sql)
	if err != nil {
		return "", fmt.Errorf("encrypt SQL for storage: %w", err)
	}
	return sealed, nil
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
