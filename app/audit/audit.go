// Package audit writes security and API access events to app.audit_logs.
// Used by the HTTP server to record API_REQUEST, AUTH_FAILURE, AUTH_SUCCESS, RATE_LIMIT_EXCEEDED.
package audit

import (
	"context"
	"encoding/json"
	"net"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/app/observability"
)

// Event types for audit_logs.event_type.
const (
	EventAPIRequest        = "API_REQUEST"
	EventAuthFailure       = "AUTH_FAILURE"
	EventAuthSuccess       = "AUTH_SUCCESS"
	EventRateLimitExceeded = "RATE_LIMIT_EXCEEDED"
	EventUnauthorized      = "UNAUTHORIZED_ACCESS"
	EventRunQuery          = "RUN_QUERY"
	EventGenerateReport    = "GENERATE_REPORT"
)

// Entry represents a single audit log row.
type Entry struct {
	EventType  string
	EntityType string
	EntityID   *string
	Details    map[string]interface{}
	UserID     string
	IP         string
	UserAgent  string
	OrgID      string
}

// Store writes audit entries to the database.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore returns an audit store that writes to the given app pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Record inserts one audit entry. It is best-effort; errors are not returned
// so that request handling is not failed by audit write failures.
func (s *Store) Record(ctx context.Context, e Entry) {
	if s == nil || s.pool == nil {
		return
	}
	detailsJSON, _ := json.Marshal(e.Details)
	var ip net.IP
	if e.IP != "" {
		ip = net.ParseIP(e.IP)
	}
	orgID := strings.TrimSpace(e.OrgID)
	if orgID == "" {
		orgID = auth.OrgIDFromContext(ctx)
	}
	if orgID == "" {
		orgID = auth.DefaultOrgID()
	}
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		observability.IncAuditWriteFailure()
		return
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT set_config('app.current_org_id', $1, false)`, orgID); err != nil {
		observability.IncAuditWriteFailure()
		return
	}
	defer func() {
		_, _ = conn.Exec(context.Background(), `SELECT set_config('app.current_org_id', '', false)`)
	}()
	_, err = conn.Exec(ctx,
		`INSERT INTO app.audit_logs (event_type, entity_type, entity_id, details, user_id, ip_address, user_agent, organization_id)
		 VALUES ($1, $2, $3, $4, NULLIF($5,''), $6, NULLIF($7,''), $8::uuid)`,
		e.EventType, e.EntityType, e.EntityID, detailsJSON, e.UserID, ip, e.UserAgent, orgID,
	)
	if err != nil {
		observability.IncAuditWriteFailure()
	}
}
