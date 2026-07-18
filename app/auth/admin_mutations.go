package auth

import (
	"context"
	"fmt"
	"strings"
)

// UpsertMembership creates or updates a user's membership in an organization.
func (s *MembershipStore) UpsertMembership(ctx context.Context, userID, orgID, role string) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("membership store is not configured")
	}
	userID = strings.TrimSpace(userID)
	orgID = strings.TrimSpace(orgID)
	if userID == "" || orgID == "" {
		return fmt.Errorf("user_id and organization_id are required")
	}
	role = normalizeRole(role)
	return execWithOrg(ctx, s.pool, orgID, `
		INSERT INTO app.organization_members (organization_id, user_id, role)
		VALUES ($1::uuid, $2, $3)
		ON CONFLICT (organization_id, user_id) DO UPDATE SET role = EXCLUDED.role
	`, orgID, userID, role)
}

// AssignConnection grants an organisation access to a connection_id.
func (a *ConnectionAuthorizer) AssignConnection(ctx context.Context, orgID, connectionID string) error {
	if a == nil || a.pool == nil {
		return fmt.Errorf("connection authorizer is not configured")
	}
	orgID = strings.TrimSpace(orgID)
	connectionID = strings.TrimSpace(connectionID)
	if orgID == "" || connectionID == "" {
		return fmt.Errorf("organization_id and connection_id are required")
	}
	return execWithOrg(ctx, a.pool, orgID, `
		INSERT INTO app.organization_connections (organization_id, connection_id, enabled)
		VALUES ($1::uuid, $2, true)
		ON CONFLICT (organization_id, connection_id) DO UPDATE SET enabled = true
	`, orgID, connectionID)
}

// GrantPermission sets action flags for a user principal on a connection.
func (a *ConnectionAuthorizer) GrantPermission(ctx context.Context, orgID, connectionID, principalID string, actions map[string]bool) error {
	if a == nil || a.pool == nil {
		return fmt.Errorf("connection authorizer is not configured")
	}
	orgID = strings.TrimSpace(orgID)
	connectionID = strings.TrimSpace(connectionID)
	principalID = strings.TrimSpace(principalID)
	if orgID == "" || connectionID == "" || principalID == "" {
		return fmt.Errorf("organization_id, connection_id, and principal_id are required")
	}
	get := func(name string) bool { return actions[name] }
	if err := execWithOrg(ctx, a.pool, orgID, `
		DELETE FROM app.connection_permissions
		WHERE organization_id = $1::uuid
		  AND connection_id = $2
		  AND principal_type = 'user'
		  AND principal_id = $3
	`, orgID, connectionID, principalID); err != nil {
		return err
	}
	return execWithOrg(ctx, a.pool, orgID, `
		INSERT INTO app.connection_permissions (
			organization_id, connection_id, principal_type, principal_id,
			can_query, can_explain, can_analyze, can_schema, can_report, can_schedule, can_stats, can_ask
		) VALUES (
			$1::uuid, $2, 'user', $3,
			$4, $5, $6, $7, $8, $9, $10, $11
		)
	`, orgID, connectionID, principalID,
		get(ActionQuery), get(ActionExplain), get(ActionAnalyze), get(ActionSchema),
		get(ActionReport), get(ActionSchedule), get(ActionStats), get(ActionAsk))
}

// RevokePermission removes a user principal's connection permission row.
func (a *ConnectionAuthorizer) RevokePermission(ctx context.Context, orgID, connectionID, principalID string) error {
	if a == nil || a.pool == nil {
		return fmt.Errorf("connection authorizer is not configured")
	}
	return execWithOrg(ctx, a.pool, orgID, `
		DELETE FROM app.connection_permissions
		WHERE organization_id = $1::uuid
		  AND connection_id = $2
		  AND principal_type = 'user'
		  AND principal_id = $3
	`, orgID, connectionID, principalID)
}
