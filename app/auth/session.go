package auth

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const sessionCookieName = "pgqn_session"

// Session holds browser session state after OIDC login.
// When a SessionStore is attached, the cookie contains only Session.ID and the
// authoritative identity lives server-side.
type Session struct {
	ID           string    `json:"sid,omitempty"`
	UserID       string    `json:"sub"`
	OrgID        string    `json:"org_id"`
	Role         string    `json:"role"`
	ExpiresAt    time.Time `json:"exp"`
	RefreshToken string    `json:"refresh_token,omitempty"`
}

// SessionManager signs and verifies HttpOnly session cookies.
type SessionManager struct {
	secret []byte
	ttl    time.Duration
	secure bool
	store  *SessionStore
}

// NewSessionManager creates a session manager when SECURITY_SESSION_SECRET is configured.
func NewSessionManager(secret string, ttl time.Duration, secure bool) *SessionManager {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return nil
	}
	if ttl <= 0 {
		ttl = 8 * time.Hour
	}
	return &SessionManager{secret: []byte(secret), ttl: ttl, secure: secure}
}

// LoadSessionManagerFromEnv builds a session manager from environment variables.
func LoadSessionManagerFromEnv() *SessionManager {
	secret := os.Getenv("SECURITY_SESSION_SECRET")
	if secret == "" {
		return nil
	}
	ttl := 8 * time.Hour
	if v := os.Getenv("SECURITY_SESSION_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			ttl = d
		}
	}
	secure := strings.EqualFold(os.Getenv("APP_ENV"), "production") ||
		strings.EqualFold(os.Getenv("APP_ENV"), "prod") ||
		strings.EqualFold(os.Getenv("SECURITY_STRICT"), "true")
	return NewSessionManager(secret, ttl, secure)
}

// Enabled reports whether browser sessions are configured.
func (m *SessionManager) Enabled() bool {
	return m != nil && len(m.secret) > 0
}

// TTL returns the configured session lifetime.
func (m *SessionManager) TTL() time.Duration {
	if m == nil {
		return 0
	}
	return m.ttl
}

// Issue writes a signed session cookie. With a SessionStore, only the session ID
// is placed in the cookie and identity is persisted server-side.
func (m *SessionManager) Issue(w http.ResponseWriter, s Session) error {
	if !m.Enabled() {
		return errors.New("session manager not configured")
	}
	if s.OrgID == "" {
		s.OrgID = DefaultOrgID()
	}
	if s.Role == "" {
		s.Role = RoleAnalyst
	}
	if s.ExpiresAt.IsZero() {
		s.ExpiresAt = time.Now().UTC().Add(m.ttl)
	}

	sealedRefresh := ""
	plainRefresh := s.RefreshToken
	if plainRefresh != "" {
		sealed, err := sealSecret(m.secret, plainRefresh)
		if err != nil {
			return err
		}
		sealedRefresh = sealed
	}

	if m.store != nil && m.store.Enabled() {
		ctx := context.Background()
		if s.ID != "" {
			if err := m.store.Update(ctx, s.ID, s, sealedRefresh); err != nil {
				return err
			}
		} else {
			id, err := m.store.Create(ctx, s, sealedRefresh)
			if err != nil {
				return err
			}
			s.ID = id
		}
		token := signPayload(m.secret, []byte(s.ID))
		http.SetCookie(w, &http.Cookie{ // #nosec G124
			Name:     sessionCookieName,
			Value:    token,
			Path:     "/",
			HttpOnly: true,
			Secure:   m.secure,
			SameSite: http.SameSiteLaxMode,
			Expires:  s.ExpiresAt,
		})
		return nil
	}

	// Legacy cookie-embedded session (no server store configured).
	s.RefreshToken = sealedRefresh
	payload, err := json.Marshal(s) // #nosec G117 -- RefreshToken is sealSecret()-encrypted above when present.
	if err != nil {
		return err
	}
	token := signPayload(m.secret, payload)
	http.SetCookie(w, &http.Cookie{ // #nosec G124
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   m.secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  s.ExpiresAt,
	})
	return nil
}

// Clear removes the session cookie.
func (m *SessionManager) Clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{ // #nosec G124
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   m.secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// Read validates the session cookie on a request.
func (m *SessionManager) Read(r *http.Request) (*Session, error) {
	if !m.Enabled() {
		return nil, errors.New("session manager not configured")
	}
	c, err := r.Cookie(sessionCookieName)
	if err != nil || strings.TrimSpace(c.Value) == "" {
		return nil, errors.New("no session cookie")
	}
	payload, err := verifySignedPayload(m.secret, c.Value)
	if err != nil {
		return nil, err
	}

	if m.store != nil && m.store.Enabled() {
		id := strings.TrimSpace(string(payload))
		// Legacy cookies were JSON payloads; migrate by rejecting until re-login when store is required.
		if strings.HasPrefix(id, "{") {
			return nil, errors.New("legacy session cookie; re-authenticate")
		}
		s, err := m.store.Load(r.Context(), id)
		if err != nil {
			return nil, err
		}
		if s.RefreshToken != "" {
			plain, openErr := openSecret(m.secret, s.RefreshToken)
			if openErr == nil {
				s.RefreshToken = plain
			} else if strings.HasPrefix(s.RefreshToken, "v1:") {
				return nil, errors.New("invalid refresh token envelope")
			}
		}
		return s, nil
	}

	var s Session
	if err := json.Unmarshal(payload, &s); err != nil {
		return nil, err
	}
	if time.Now().UTC().After(s.ExpiresAt) {
		return nil, errors.New("session expired")
	}
	if strings.TrimSpace(s.UserID) == "" {
		return nil, errors.New("empty session subject")
	}
	if s.OrgID == "" {
		s.OrgID = DefaultOrgID()
	}
	if s.RefreshToken != "" {
		plain, err := openSecret(m.secret, s.RefreshToken)
		if err != nil {
			if !strings.HasPrefix(s.RefreshToken, "v1:") {
				// leave as-is (legacy plaintext)
			} else {
				return nil, errors.New("invalid refresh token envelope")
			}
		} else {
			s.RefreshToken = plain
		}
	}
	return &s, nil
}

// RefreshHandler extends a valid session or uses the OIDC refresh token when near expiry.
func (m *SessionManager) RefreshHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if !m.Enabled() {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"refreshed":false,"reason":"sessions_disabled"}`))
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	s, err := m.Read(r)
	if err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"refreshed":false}`))
		return
	}
	s.ExpiresAt = time.Now().UTC().Add(m.ttl)
	if err := m.Issue(w, *s); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"refreshed":false,"reason":"issue_failed"}`))
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"refreshed":true}`))
}

// StatusHandler returns the current browser session as JSON.
func (m *SessionManager) StatusHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if !m.Enabled() {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"authenticated":false,"reason":"sessions_disabled"}`))
		return
	}
	s, err := m.Read(r)
	if err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"authenticated":false}`))
		return
	}
	_, _ = w.Write([]byte(fmt.Sprintf(
		`{"authenticated":true,"user_id":%q,"org_id":%q,"role":%q}`,
		s.UserID, s.OrgID, s.Role,
	)))
}

// NewPKCEVerifier returns a URL-safe PKCE verifier and S256 challenge.
func NewPKCEVerifier() (verifier, challenge string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}

func parseSessionCookieID(secret []byte, token string) (string, bool) {
	payload, err := verifySignedPayload(secret, token)
	if err != nil {
		return "", false
	}
	id := strings.TrimSpace(string(payload))
	if id == "" || strings.HasPrefix(id, "{") {
		return "", false
	}
	return id, true
}

func signPayload(secret, payload []byte) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(payload)
	sig := mac.Sum(nil)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func verifySignedPayload(secret []byte, token string) ([]byte, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return nil, errors.New("invalid session token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(payload)
	expected := mac.Sum(nil)
	if subtle.ConstantTimeCompare(sig, expected) != 1 {
		return nil, errors.New("invalid session signature")
	}
	return payload, nil
}

func sealSecret(secret []byte, plaintext string) (string, error) {
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

func openSecret(secret []byte, sealed string) (string, error) {
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
