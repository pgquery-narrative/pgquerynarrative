package auth

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// KeyUsageStore persists API key last-used timestamps so admin listings survive
// process restarts and work across replicas (unlike the in-memory sync.Map alone).
type KeyUsageStore struct {
	pool *pgxpool.Pool
}

// NewKeyUsageStore returns a durable last-used store backed by app.api_key_usage.
func NewKeyUsageStore(pool *pgxpool.Pool) *KeyUsageStore {
	if pool == nil {
		return nil
	}
	return &KeyUsageStore{pool: pool}
}

// Touch records a successful authentication for keyID (best-effort, non-blocking to callers).
func (s *KeyUsageStore) Touch(ctx context.Context, keyID string) {
	if s == nil || s.pool == nil || keyID == "" {
		return
	}
	_, _ = s.pool.Exec(ctx, `
		INSERT INTO app.api_key_usage (key_id, last_used_at, use_count)
		VALUES ($1, NOW(), 1)
		ON CONFLICT (key_id) DO UPDATE SET
			last_used_at = EXCLUDED.last_used_at,
			use_count = app.api_key_usage.use_count + 1
	`, keyID)
}

// LastUsedAt returns the durable last-used timestamp for keyID when present.
func (s *KeyUsageStore) LastUsedAt(ctx context.Context, keyID string) (time.Time, bool) {
	if s == nil || s.pool == nil || keyID == "" {
		return time.Time{}, false
	}
	var t time.Time
	err := s.pool.QueryRow(ctx, `SELECT last_used_at FROM app.api_key_usage WHERE key_id = $1`, keyID).Scan(&t)
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

// LoadAll returns last-used timestamps for every tracked key (for admin metadata listings).
func (s *KeyUsageStore) LoadAll(ctx context.Context) map[string]time.Time {
	out := map[string]time.Time{}
	if s == nil || s.pool == nil {
		return out
	}
	rows, err := s.pool.Query(ctx, `SELECT key_id, last_used_at FROM app.api_key_usage`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var t time.Time
		if err := rows.Scan(&id, &t); err != nil {
			continue
		}
		out[id] = t.UTC()
	}
	return out
}

// SetKeyUsageStore attaches durable last-used tracking to the authenticator.
func (a *Authenticator) SetKeyUsageStore(store *KeyUsageStore) {
	if a != nil {
		a.keyUsage = store
	}
}

// warmLastUsed hydrates the in-memory cache from durable storage once.
func (a *Authenticator) warmLastUsed() {
	if a == nil || a.keyUsage == nil {
		return
	}
	a.usageWarm.Do(func() {
		for id, t := range a.keyUsage.LoadAll(context.Background()) {
			a.lastUsed.Store(id, t)
		}
	})
}
