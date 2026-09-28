package auth

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PKCEStore persists OIDC PKCE state for multi-replica browser login.
type PKCEStore struct {
	pool *pgxpool.Pool
}

// NewPKCEStore returns a PostgreSQL-backed PKCE store.
func NewPKCEStore(pool *pgxpool.Pool) *PKCEStore {
	if pool == nil {
		return nil
	}
	return &PKCEStore{pool: pool}
}

// Save stores PKCE verifier for the given OAuth state.
func (s *PKCEStore) Save(ctx context.Context, state, verifier string, ttl time.Duration) error {
	if s == nil || s.pool == nil {
		return nil
	}
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	expires := time.Now().UTC().Add(ttl)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO app.oidc_pkce_states (state, verifier, expires_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (state) DO UPDATE SET verifier = EXCLUDED.verifier, expires_at = EXCLUDED.expires_at
	`, state, verifier, expires)
	return err
}

// Consume returns and deletes the verifier for state when still valid.
func (s *PKCEStore) Consume(ctx context.Context, state string) (verifier string, ok bool) {
	if s == nil || s.pool == nil {
		return "", false
	}
	err := s.pool.QueryRow(ctx, `
		DELETE FROM app.oidc_pkce_states
		WHERE state = $1 AND expires_at > NOW()
		RETURNING verifier
	`, state).Scan(&verifier)
	if err != nil {
		return "", false
	}
	return verifier, true
}

// CleanupExpired removes stale PKCE rows (best-effort).
func (s *PKCEStore) CleanupExpired(ctx context.Context) {
	if s == nil || s.pool == nil {
		return
	}
	_, _ = s.pool.Exec(ctx, `DELETE FROM app.oidc_pkce_states WHERE expires_at <= NOW()`)
}

// ErrPKCEStateMissing indicates no valid PKCE state was found.
var ErrPKCEStateMissing = pgx.ErrNoRows
