package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strings"
	"time"
)

// HashAPIKey returns the lowercase hex SHA-256 digest of an API key secret.
func HashAPIKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func matchesAPIKey(provided string, entry APIKeyEntry) bool {
	provided = strings.TrimSpace(provided)
	if provided == "" {
		return false
	}
	if h := strings.TrimSpace(entry.KeyHash); h != "" {
		digest := HashAPIKey(provided)
		return subtle.ConstantTimeCompare([]byte(strings.ToLower(digest)), []byte(strings.ToLower(h))) == 1
	}
	if k := strings.TrimSpace(entry.Key); k != "" {
		return subtle.ConstantTimeCompare([]byte(provided), []byte(k)) == 1
	}
	return false
}

func entryExpired(entry APIKeyEntry, now time.Time) bool {
	if entry.ExpiresAt.IsZero() {
		return false
	}
	return now.UTC().After(entry.ExpiresAt.UTC())
}

func entryAllowsRequest(entry APIKeyEntry, method, path string) bool {
	if len(entry.Scopes) == 0 {
		return AllowsMethod(entry.Role, method, path)
	}
	method = strings.ToUpper(method)
	for _, scope := range entry.Scopes {
		switch strings.ToLower(strings.TrimSpace(scope)) {
		case "admin":
			return AllowsMethod(RoleAdmin, method, path)
		case "write":
			if AllowsMethod(RoleAnalyst, method, path) {
				return true
			}
		case "read":
			if method == "GET" || method == "HEAD" || method == "OPTIONS" {
				return true
			}
		}
	}
	return false
}
