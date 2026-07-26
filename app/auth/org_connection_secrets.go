package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/app/security"
)

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

// OpenDSN decrypts the DSN for (org, connection) when an enabled secret exists.
// Returns ok=false when no secret is configured (caller should use catalog pool).
func (s *OrgConnectionSecretStore) OpenDSN(ctx context.Context, orgID, connectionID string) (dsn string, schemas []string, ok bool, err error) {
	if s == nil || s.pool == nil {
		return "", nil, false, nil
	}
	orgID = strings.TrimSpace(orgID)
	connectionID = strings.TrimSpace(connectionID)
	if orgID == "" || connectionID == "" {
		return "", nil, false, nil
	}
	if len(s.encKey) == 0 {
		return "", nil, false, fmt.Errorf("SECURITY_DATA_ENCRYPTION_KEY is required to open connection secrets")
	}
	var sealed string
	var raw []byte
	var enabled bool
	scanErr := queryRowWithOrg(ctx, s.pool, orgID, `
		SELECT sealed_dsn, allowed_schemas, enabled
		FROM app.organization_connection_secrets
		WHERE organization_id = $1::uuid AND connection_id = $2
	`, orgID, connectionID)(&sealed, &raw, &enabled)
	if scanErr != nil {
		if scanErr == pgx.ErrNoRows {
			return "", nil, false, nil
		}
		return "", nil, false, scanErr
	}
	if !enabled {
		return "", nil, false, nil
	}
	plain, openErr := security.Open(s.encKey, sealed)
	if openErr != nil {
		return "", nil, false, openErr
	}
	_ = json.Unmarshal(raw, &schemas)
	return plain, schemas, true, nil
}
