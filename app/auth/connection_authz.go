package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	apperrors "github.com/pgquerynarrative/pgquerynarrative/app/errors"
)

// Connection action names used for authorization checks.
const (
	ActionQuery    = "query"
	ActionExplain  = "explain"
	ActionAnalyze  = "analyze"
	ActionSchema   = "schema"
	ActionReport   = "report"
	ActionSchedule = "schedule"
	ActionStats    = "stats"
	ActionAsk      = "ask"
)

// Aliases retained for callers that used ConnAction* naming.
const (
	ConnActionQuery    = ActionQuery
	ConnActionExplain  = ActionExplain
	ConnActionAnalyze  = ActionAnalyze
	ConnActionSchema   = ActionSchema
	ConnActionReport   = ActionReport
	ConnActionSchedule = ActionSchedule
	ConnActionStats    = ActionStats
	ConnActionAsk      = ActionAsk
)

// ErrConnectionForbidden indicates the principal may not use the connection for the action.
var ErrConnectionForbidden = apperrors.ErrConnectionForbidden

// connectionActionColumns maps action names to connection_permissions boolean columns.
var connectionActionColumns = map[string]string{
	ActionQuery:    "query",
	ActionExplain:  "explain",
	ActionAnalyze:  "analyze",
	ActionSchema:   "schema",
	ActionReport:   "report",
	ActionSchedule: "schedule",
	ActionStats:    "stats",
	ActionAsk:      "ask",
}

// ConnectionAuthorizer checks organisation and principal permissions for DB connections.
type ConnectionAuthorizer struct {
	pool *pgxpool.Pool
}

// NewConnectionAuthorizer returns an authorizer backed by the app pool.
func NewConnectionAuthorizer(pool *pgxpool.Pool) *ConnectionAuthorizer {
	if pool == nil {
		return nil
	}
	return &ConnectionAuthorizer{pool: pool}
}

// decideConnectionAuthz is the pure decision matrix (unit-tested without a database).
func decideConnectionAuthz(orgHasAnyRows, connectionAssigned bool, role string, principalGranted bool) error {
	if !orgHasAnyRows {
		return nil // bootstrap
	}
	if !connectionAssigned {
		return fmt.Errorf("%w: connection not assigned to organisation", ErrConnectionForbidden)
	}
	if normalizeRole(role) == RoleAdmin {
		return nil
	}
	if !principalGranted {
		return fmt.Errorf("%w: missing permission", ErrConnectionForbidden)
	}
	return nil
}

// AuthorizeConnection verifies org membership may use connectionID for action.
func (a *ConnectionAuthorizer) AuthorizeConnection(ctx context.Context, orgID, userID, role, connectionID, action string) error {
	if a == nil || a.pool == nil {
		return nil
	}
	orgID = strings.TrimSpace(orgID)
	connectionID = strings.TrimSpace(connectionID)
	action = strings.ToLower(strings.TrimSpace(action))
	if orgID == "" || connectionID == "" {
		return fmt.Errorf("%w: missing org or connection", ErrConnectionForbidden)
	}
	col, ok := connectionActionColumns[action]
	if !ok {
		return fmt.Errorf("%w: unknown action %q", ErrConnectionForbidden, action)
	}
	role = normalizeRole(role)

	var connCount int
	if err := a.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM app.organization_connections WHERE organization_id = $1::uuid
	`, orgID).Scan(&connCount); err != nil {
		return err
	}
	orgHasAnyRows := connCount > 0
	connectionAssigned := false
	if orgHasAnyRows {
		var enabled bool
		err := a.pool.QueryRow(ctx, `
			SELECT enabled FROM app.organization_connections
			WHERE organization_id = $1::uuid AND connection_id = $2
		`, orgID, connectionID).Scan(&enabled)
		if err == nil && enabled {
			connectionAssigned = true
		} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}

	principalGranted := false
	if role != RoleAdmin {
		q := fmt.Sprintf(`
			SELECT EXISTS (
				SELECT 1 FROM app.connection_permissions
				WHERE organization_id = $1::uuid
				  AND (connection_id IS NULL OR connection_id = $2)
				  AND %s = true
				  AND (
				    (principal_type = 'user' AND principal_id = $3)
				    OR (principal_type = 'role' AND principal_id = $4)
				  )
			)
		`, col)
		if err := a.pool.QueryRow(ctx, q, orgID, connectionID, strings.TrimSpace(userID), role).Scan(&principalGranted); err != nil {
			return err
		}
	}

	return decideConnectionAuthz(orgHasAnyRows, connectionAssigned, role, principalGranted)
}

// AllowedConnections returns configured connection IDs the principal may use for action.
func (a *ConnectionAuthorizer) AllowedConnections(ctx context.Context, orgID, userID, role string, configured []string, action string) []string {
	if a == nil || a.pool == nil {
		return configured
	}
	out := make([]string, 0, len(configured))
	for _, id := range configured {
		if err := a.AuthorizeConnection(ctx, orgID, userID, role, id, action); err == nil {
			out = append(out, id)
		}
	}
	return out
}

// ListAllowedConnections is an alias that returns an error for interface compatibility.
func (a *ConnectionAuthorizer) ListAllowedConnections(ctx context.Context, orgID, userID, role string, configured []string, action string) ([]string, error) {
	return a.AllowedConnections(ctx, orgID, userID, role, configured, action), nil
}
