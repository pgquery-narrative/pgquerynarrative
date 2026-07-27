package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/app/security"
)

// OrgConnectionMode distinguishes dedicated tenant DSN states from shared fallback.
type OrgConnectionMode string

const (
	// OrgConnectionNoOverride means no tenant secret row exists; shared catalog pool is allowed.
	OrgConnectionNoOverride OrgConnectionMode = "no_override"
	// OrgConnectionDedicated means a decryptable enabled tenant DSN is available.
	OrgConnectionDedicated OrgConnectionMode = "dedicated"
	// OrgConnectionDisabled means a tenant secret exists but is disabled; never use shared pool.
	OrgConnectionDisabled OrgConnectionMode = "disabled"
	// OrgConnectionKeyUnavailable means a tenant secret exists but the encryption key is missing.
	OrgConnectionKeyUnavailable OrgConnectionMode = "key_unavailable"
	// OrgConnectionDecryptFailed means a tenant secret exists but cannot be decrypted.
	OrgConnectionDecryptFailed OrgConnectionMode = "decrypt_failed"
)

// OrgConnectionResolution is the authoritative result of resolving a tenant analytics DSN.
type OrgConnectionResolution struct {
	Mode    OrgConnectionMode
	DSN     string
	Schemas []string
	Version int64
}

// FailClosed reports whether this resolution must never fall back to the shared catalog pool.
func (r OrgConnectionResolution) FailClosed() bool {
	switch r.Mode {
	case OrgConnectionDisabled, OrgConnectionKeyUnavailable, OrgConnectionDecryptFailed:
		return true
	default:
		return false
	}
}

// Error returns a caller-facing error for fail-closed modes.
func (r OrgConnectionResolution) Error() error {
	switch r.Mode {
	case OrgConnectionDisabled:
		return fmt.Errorf("organisation connection is disabled")
	case OrgConnectionKeyUnavailable:
		return fmt.Errorf("SECURITY_DATA_ENCRYPTION_KEY is required to open organisation connection secrets")
	case OrgConnectionDecryptFailed:
		return fmt.Errorf("organisation connection secret could not be decrypted")
	default:
		return nil
	}
}

// OrgConnectionSecretStore manages per-organisation encrypted analytics DSNs.
type OrgConnectionSecretStore struct {
	pool   *pgxpool.Pool
	encKey []byte
}

// NewOrgConnectionSecretStore creates a secret store. encKey must be non-empty to seal DSNs.
func NewOrgConnectionSecretStore(pool *pgxpool.Pool, encKey string) *OrgConnectionSecretStore {
	if pool == nil {
		return nil
	}
	return &OrgConnectionSecretStore{pool: pool, encKey: []byte(strings.TrimSpace(encKey))}
}

// ConnectionSecretMeta is a non-sensitive listing of a stored org DSN.
type ConnectionSecretMeta struct {
	ConnectionID   string   `json:"connection_id"`
	AllowedSchemas []string `json:"allowed_schemas"`
	Enabled        bool     `json:"enabled"`
}

// Upsert seals and stores a postgres DSN for (org, connection_id).
func (s *OrgConnectionSecretStore) Upsert(ctx context.Context, orgID, connectionID, dsn string, allowedSchemas []string) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("connection secrets store is not configured")
	}
	if len(s.encKey) == 0 {
		return fmt.Errorf("SECURITY_DATA_ENCRYPTION_KEY is required to store connection secrets")
	}
	orgID = strings.TrimSpace(orgID)
	connectionID = strings.TrimSpace(connectionID)
	dsn = strings.TrimSpace(dsn)
	if orgID == "" || connectionID == "" || dsn == "" {
		return fmt.Errorf("organization_id, connection_id, and dsn are required")
	}
	if len(allowedSchemas) == 0 {
		return fmt.Errorf("allowed_schemas must be non-empty for organisation connection secrets")
	}
	sealed, err := security.Seal(s.encKey, dsn)
	if err != nil {
		return fmt.Errorf("seal dsn: %w", err)
	}
	schemasJSON, err := json.Marshal(allowedSchemas)
	if err != nil {
		return err
	}
	return execWithOrg(ctx, s.pool, orgID, `
		INSERT INTO app.organization_connection_secrets (
			organization_id, connection_id, sealed_dsn, allowed_schemas, enabled, updated_at
		) VALUES ($1::uuid, $2, $3, $4::jsonb, true, NOW())
		ON CONFLICT (organization_id, connection_id) DO UPDATE SET
			sealed_dsn = EXCLUDED.sealed_dsn,
			allowed_schemas = EXCLUDED.allowed_schemas,
			enabled = true,
			updated_at = NOW()
	`, orgID, connectionID, sealed, string(schemasJSON))
}

// Delete removes a stored org DSN.
func (s *OrgConnectionSecretStore) Delete(ctx context.Context, orgID, connectionID string) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("connection secrets store is not configured")
	}
	orgID = strings.TrimSpace(orgID)
	connectionID = strings.TrimSpace(connectionID)
	if orgID == "" || connectionID == "" {
		return fmt.Errorf("organization_id and connection_id are required")
	}
	return execWithOrg(ctx, s.pool, orgID, `
		DELETE FROM app.organization_connection_secrets
		WHERE organization_id = $1::uuid AND connection_id = $2
	`, orgID, connectionID)
}

// List returns metadata (never the DSN) for an organisation's secrets.
func (s *OrgConnectionSecretStore) List(ctx context.Context, orgID string) ([]ConnectionSecretMeta, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("connection secrets store is not configured")
	}
	orgID = strings.TrimSpace(orgID)
	if orgID == "" {
		return nil, fmt.Errorf("organization_id is required")
	}
	rows, err := queryWithOrg(ctx, s.pool, orgID, `
		SELECT connection_id, allowed_schemas, enabled
		FROM app.organization_connection_secrets
		WHERE organization_id = $1::uuid
		ORDER BY connection_id
	`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ConnectionSecretMeta
	for rows.Next() {
		var m ConnectionSecretMeta
		var raw []byte
		if err := rows.Scan(&m.ConnectionID, &raw, &m.Enabled); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(raw, &m.AllowedSchemas)
		if m.AllowedSchemas == nil {
			m.AllowedSchemas = []string{}
		}
		out = append(out, m)
	}
	if out == nil {
		out = []ConnectionSecretMeta{}
	}
	return out, rows.Err()
}

// CountSecrets returns how many organisation connection secrets exist across all orgs.
// Used at startup to require SECURITY_DATA_ENCRYPTION_KEY whenever tenant DSNs are present.
func (s *OrgConnectionSecretStore) CountSecrets(ctx context.Context) (int64, error) {
	if s == nil || s.pool == nil {
		return 0, nil
	}
	var n int64
	err := s.pool.QueryRow(ctx, `SELECT app.count_organization_connection_secrets()`).Scan(&n)
	if err != nil {
		// Older deployments may lack the helper; best-effort direct count (may be RLS-filtered).
		err2 := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM app.organization_connection_secrets`).Scan(&n)
		if err2 != nil {
			return 0, err
		}
		return n, nil
	}
	return n, nil
}

// Resolve returns the authoritative organisation connection resolution for (org, connection).
// Only OrgConnectionNoOverride may fall back to the shared catalog pool.
func (s *OrgConnectionSecretStore) Resolve(ctx context.Context, orgID, connectionID string) (OrgConnectionResolution, error) {
	if s == nil || s.pool == nil {
		return OrgConnectionResolution{Mode: OrgConnectionNoOverride}, nil
	}
	orgID = strings.TrimSpace(orgID)
	connectionID = strings.TrimSpace(connectionID)
	if orgID == "" || connectionID == "" {
		return OrgConnectionResolution{Mode: OrgConnectionNoOverride}, nil
	}

	var sealed string
	var raw []byte
	var enabled bool
	var updatedAt time.Time
	scanErr := queryRowWithOrg(ctx, s.pool, orgID, `
		SELECT sealed_dsn, allowed_schemas, enabled, updated_at
		FROM app.organization_connection_secrets
		WHERE organization_id = $1::uuid AND connection_id = $2
	`, orgID, connectionID)(&sealed, &raw, &enabled, &updatedAt)
	if scanErr != nil {
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return OrgConnectionResolution{Mode: OrgConnectionNoOverride}, nil
		}
		return OrgConnectionResolution{}, scanErr
	}

	schemas := decodeSchemaList(raw)
	version := updatedAt.UnixNano()
	base := OrgConnectionResolution{Schemas: schemas, Version: version}

	if !enabled {
		base.Mode = OrgConnectionDisabled
		return base, nil
	}
	if len(s.encKey) == 0 {
		base.Mode = OrgConnectionKeyUnavailable
		return base, nil
	}
	plain, openErr := security.Open(s.encKey, sealed)
	if openErr != nil {
		base.Mode = OrgConnectionDecryptFailed
		return base, nil
	}
	base.Mode = OrgConnectionDedicated
	base.DSN = plain
	return base, nil
}

// OpenDSN decrypts the DSN for (org, connection) when an enabled secret exists.
// Returns ok=false only for OrgConnectionNoOverride. Fail-closed modes return an error.
func (s *OrgConnectionSecretStore) OpenDSN(ctx context.Context, orgID, connectionID string) (dsn string, schemas []string, ok bool, err error) {
	res, err := s.Resolve(ctx, orgID, connectionID)
	if err != nil {
		return "", nil, false, err
	}
	switch res.Mode {
	case OrgConnectionNoOverride:
		return "", nil, false, nil
	case OrgConnectionDedicated:
		return res.DSN, res.Schemas, true, nil
	default:
		return "", res.Schemas, false, res.Error()
	}
}

// AllowedSchemas returns the tenant-specific schema allowlist for a stored DSN.
// ok=true whenever a secret row exists (including disabled / key-unavailable), so callers
// treat the allowlist as authoritative and do not fall back to catalog schemas.
func (s *OrgConnectionSecretStore) AllowedSchemas(ctx context.Context, orgID, connectionID string) (schemas []string, ok bool, err error) {
	res, err := s.Resolve(ctx, orgID, connectionID)
	if err != nil {
		return nil, false, err
	}
	switch res.Mode {
	case OrgConnectionNoOverride:
		return nil, false, nil
	default:
		return append([]string(nil), res.Schemas...), true, nil
	}
}

func decodeSchemaList(raw []byte) []string {
	var schemas []string
	_ = json.Unmarshal(raw, &schemas)
	if schemas == nil {
		return []string{}
	}
	return schemas
}
