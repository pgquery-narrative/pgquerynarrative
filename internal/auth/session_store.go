package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SessionStore persists revocable browser sessions server-side.
type SessionStore struct {
	pool *pgxpool.Pool
}

// NewSessionStore creates a server-side session store. Nil pool disables persistence.
func NewSessionStore(pool *pgxpool.Pool) *SessionStore {
	if pool == nil {
		return nil
	}
	return &SessionStore{pool: pool}
}

// Enabled reports whether the store is usable.
func (s *SessionStore) Enabled() bool {
	return s != nil && s.pool != nil
}

func newSessionUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

// Create inserts a new server-side session and returns its ID.
func (s *SessionStore) Create(ctx context.Context, sess Session, sealedRefresh string) (string, error) {
	if !s.Enabled() {
		return "", errors.New("session store not configured")
	}
	id, err := newSessionUUID()
	if err != nil {
		return "", err
	}
	_, err = s.pool.Exec(ctx, `
		SELECT app.insert_browser_session($1::uuid, $2, $3::uuid, $4, $5, NULLIF($6, ''))
	`, id, sess.UserID, sess.OrgID, sess.Role, sess.ExpiresAt.UTC(), sealedRefresh)
	if err != nil {
		return "", err
	}
	return id, nil
}

// Update refreshes an existing session row.
func (s *SessionStore) Update(ctx context.Context, id string, sess Session, sealedRefresh string) error {
	if !s.Enabled() {
		return errors.New("session store not configured")
	}
	_, err := s.pool.Exec(ctx, `
		SELECT app.update_browser_session($1::uuid, $2::uuid, $3, $4, NULLIF($5, ''))
	`, id, sess.OrgID, sess.Role, sess.ExpiresAt.UTC(), sealedRefresh)
	return err
}

// Load returns a non-revoked, non-expired session by ID.
func (s *SessionStore) Load(ctx context.Context, id string) (*Session, error) {
	if !s.Enabled() {
		return nil, errors.New("session store not configured")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, errors.New("empty session id")
	}
	var userID, orgID, role string
	var expiresAt time.Time
	var revokedAt *time.Time
	var sealedRefresh *string
	err := s.pool.QueryRow(ctx, `
		SELECT user_id, organization_id::text, role, expires_at, revoked_at, sealed_refresh_token
		FROM app.get_browser_session($1::uuid)
	`, id).Scan(&userID, &orgID, &role, &expiresAt, &revokedAt, &sealedRefresh)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errors.New("session not found")
		}
		return nil, err
	}
	if revokedAt != nil {
		return nil, errors.New("session revoked")
	}
	if time.Now().UTC().After(expiresAt.UTC()) {
		return nil, errors.New("session expired")
	}
	_, _ = s.pool.Exec(ctx, `SELECT app.touch_browser_session($1::uuid)`, id)
	out := &Session{
		ID:        id,
		UserID:    userID,
		OrgID:     orgID,
		Role:      role,
		ExpiresAt: expiresAt.UTC(),
	}
	if sealedRefresh != nil {
		out.RefreshToken = *sealedRefresh
	}
	return out, nil
}

// Revoke marks a single session revoked.
func (s *SessionStore) Revoke(ctx context.Context, id string) error {
	if !s.Enabled() {
		return nil
	}
	_, err := s.pool.Exec(ctx, `SELECT app.revoke_browser_session($1::uuid)`, id)
	return err
}

// RevokeUserSessions revokes all active sessions for a user, optionally scoped to one org.
func (s *SessionStore) RevokeUserSessions(ctx context.Context, userID, orgID string) (int64, error) {
	if !s.Enabled() {
		return 0, nil
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return 0, fmt.Errorf("user_id is required")
	}
	var n int64
	var err error
	if strings.TrimSpace(orgID) == "" {
		err = s.pool.QueryRow(ctx, `SELECT app.revoke_browser_sessions_for_user($1, NULL)`, userID).Scan(&n)
	} else {
		err = s.pool.QueryRow(ctx, `SELECT app.revoke_browser_sessions_for_user($1, $2::uuid)`, userID, orgID).Scan(&n)
	}
	return n, err
}

// RandomSessionID returns a high-entropy opaque session identifier for tests/helpers.
func RandomSessionID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// AttachSessionStore wires a server-side store into the cookie session manager.
func (m *SessionManager) AttachSessionStore(store *SessionStore) {
	if m == nil {
		return
	}
	m.store = store
}

// ServerStore returns the attached server-side session store, if any.
func (m *SessionManager) ServerStore() *SessionStore {
	if m == nil {
		return nil
	}
	return m.store
}

// RevokeCurrent clears the cookie and revokes the server-side session when present.
func (m *SessionManager) RevokeCurrent(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if m == nil {
		return
	}
	if m.store != nil && m.store.Enabled() {
		if c, err := r.Cookie(sessionCookieName); err == nil {
			if id, ok := parseSessionCookieID(m.secret, c.Value); ok {
				_ = m.store.Revoke(ctx, id)
			}
		}
	}
	m.Clear(w)
}
