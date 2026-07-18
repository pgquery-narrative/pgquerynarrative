// Package security provides SSRF-hardened webhook delivery and shared
// cryptographic helpers used for at-rest sealing of sensitive fields.
package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"strings"
)

// Seal encrypts plaintext with AES-GCM using a key derived from secret via SHA-256.
// The returned envelope is "v1:" + base64url(nonce||ciphertext).
func Seal(secret []byte, plaintext string) (string, error) {
	if len(secret) == 0 {
		return "", errors.New("encryption secret is required")
	}
	key := sha256.Sum256(secret)
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	out := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return "v1:" + base64.RawURLEncoding.EncodeToString(out), nil
}

// Open decrypts a Seal envelope. Non-v1 envelopes return an error.
func Open(secret []byte, sealed string) (string, error) {
	if len(secret) == 0 {
		return "", errors.New("encryption secret is required")
	}
	if !strings.HasPrefix(sealed, "v1:") {
		return "", errors.New("unsupported envelope")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(sealed, "v1:"))
	if err != nil {
		return "", err
	}
	key := sha256.Sum256(secret)
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("ciphertext too short")
	}
	nonce, ciphertext := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// IsSealed reports whether s looks like a Seal envelope.
func IsSealed(s string) bool {
	return strings.HasPrefix(s, "v1:")
}
