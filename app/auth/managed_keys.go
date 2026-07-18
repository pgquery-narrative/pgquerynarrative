package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ManagedKeyStore persists hashed API keys for CLI/MCP/automation (never browser).
type ManagedKeyStore struct {
	pool *pgxpool.Pool
}

// NewManagedKeyStore returns a store backed by app.managed_api_keys.
func NewManagedKeyStore(pool *pgxpool.Pool) *ManagedKeyStore {
	if pool == nil {
		return nil
	}
	return &ManagedKeyStore{pool: pool}
}

// ManagedKey is a persisted API key without the secret.
type ManagedKey struct {
	ID        string
	OrgID     string
	Prefix    string
	Role      string
	Scopes    []string
	ExpiresAt time.Time
	RevokedAt time.Time
	CreatedBy string
	CreatedAt time.Time
}

// IssuedKey is returned only at creation time and includes the plaintext secret once.
type IssuedKey struct {
	ManagedKey
	Secret string
}

// Create issues a new API key. The plaintext secret is returned once and never stored.
func (s *ManagedKeyStore) Create(ctx context.Context, orgID, role, createdBy string, scopes []string, expiresAt time.Time) (*IssuedKey, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("managed key store is not configured")
	}
	orgID = strings.TrimSpace(orgID)
	if orgID == "" {
		return nil, fmt.Errorf("organization_id is required")
	}
	role = normalizeRole(role)
	secret, err := newAPIKeySecret()
	if err != nil {
		return nil, err
	}
	prefix := keyPrefix(secret)
	hash := HashAPIKey(secret)
	var expires any
	if !expiresAt.IsZero() {
		expires = expiresAt.UTC()
	}
	var id string
	var createdAt time.Time
	err = queryRowWithOrg(ctx, s.pool, orgID, `
		INSERT INTO app.managed_api_keys (
			organization_id, key_hash, prefix, role, scopes, expires_at, created_by
		) VALUES ($1::uuid, $2, $3, $4, $5, $6, $7)
		RETURNING id::text, created_at
	`, orgID, hash, prefix, role, scopes, expires, createdBy)(&id, &createdAt)
	if err != nil {
		return nil, err
	}
	return &IssuedKey{
		ManagedKey: ManagedKey{
			ID:        id,
			OrgID:     orgID,
			Prefix:    prefix,
			Role:      role,
			Scopes:    append([]string(nil), scopes...),
			ExpiresAt: expiresAt.UTC(),
			CreatedBy: createdBy,
			CreatedAt: createdAt.UTC(),
		},
		Secret: secret,
	}, nil
}

// List returns non-secret metadata for keys in orgID.
func (s *ManagedKeyStore) List(ctx context.Context, orgID string) ([]ManagedKey, error) {
	if s == nil || s.pool == nil {
		return nil, nil
	}
	var out []ManagedKey
	err := withOrgTx(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, qerr := tx.Query(ctx, `
			SELECT id::text, organization_id::text, prefix, role, scopes, expires_at, revoked_at, COALESCE(created_by, ''), created_at
			FROM app.managed_api_keys
			WHERE organization_id = $1::uuid
			ORDER BY created_at DESC
		`, orgID)
		if qerr != nil {
			return qerr
		}
		defer rows.Close()
		out = make([]ManagedKey, 0)
		for rows.Next() {
			var k ManagedKey
			var expires, revoked sql.NullTime
			var scopes []string
			if scanErr := rows.Scan(&k.ID, &k.OrgID, &k.Prefix, &k.Role, &scopes, &expires, &revoked, &k.CreatedBy, &k.CreatedAt); scanErr != nil {
				return scanErr
			}
			k.Scopes = scopes
			if expires.Valid {
				k.ExpiresAt = expires.Time.UTC()
			}
			if revoked.Valid {
				k.RevokedAt = revoked.Time.UTC()
			}
			k.CreatedAt = k.CreatedAt.UTC()
			out = append(out, k)
		}
		return rows.Err()
	})
	return out, err
}

// Revoke marks a key revoked. Returns false when not found or already revoked.
func (s *ManagedKeyStore) Revoke(ctx context.Context, orgID, keyID string) (bool, error) {
	if s == nil || s.pool == nil {
		return false, fmt.Errorf("managed key store is not configured")
	}
	var id string
	err := queryRowWithOrg(ctx, s.pool, orgID, `
		UPDATE app.managed_api_keys
		SET revoked_at = NOW()
		WHERE id = $1::uuid AND organization_id = $2::uuid AND revoked_at IS NULL
		RETURNING id::text
	`, keyID, orgID)(&id)
	if err != nil {
		if err == pgx.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	return id != "", nil
}

// LookupBySecret resolves a plaintext API key to a principal-ready entry.
func (s *ManagedKeyStore) LookupBySecret(ctx context.Context, secret string) (APIKeyEntry, bool, error) {
	if s == nil || s.pool == nil {
		return APIKeyEntry{}, false, nil
	}
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return APIKeyEntry{}, false, nil
	}
	hash := HashAPIKey(secret)
	var (
		id, orgID, role, prefix string
		scopes                  []string
		expires, revoked        sql.NullTime
	)
	err := s.pool.QueryRow(ctx, `
		SELECT id::text, organization_id::text, role, prefix, scopes, expires_at, revoked_at
		FROM app.resolve_managed_api_key($1)
	`, hash).Scan(&id, &orgID, &role, &prefix, &scopes, &expires, &revoked)
	if err != nil {
		if err == pgx.ErrNoRows {
			return APIKeyEntry{}, false, nil
		}
		return APIKeyEntry{}, false, err
	}
	entry := APIKeyEntry{
		KeyHash: hash,
		ID:      id,
		Prefix:  prefix,
		Role:    role,
		OrgID:   orgID,
		Scopes:  scopes,
		Revoked: revoked.Valid,
	}
	if expires.Valid {
		entry.ExpiresAt = expires.Time.UTC()
	}
	return entry, true, nil
}

func newAPIKeySecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "pgqn_" + base64.RawURLEncoding.EncodeToString(b), nil
}
