package service

import (
	"context"
	"fmt"

	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
)

// visibleResourcePredicate returns a SQL AND-clause fragment using the provided
// parameter indexes for organization_id ($orgParam) and user_id ($userParam).
// Private rows are visible only to their creator; organisation rows are visible to members.
func visibleResourcePredicate(orgParam, userParam int, role string) string {
	if auth.IsAdminRole(role) {
		// Callers always bind userParam; reference it with an explicit cast so
		// PostgreSQL can infer the parameter type (unused params error with 42P18).
		return fmt.Sprintf("organization_id = $%d AND ($%d::text IS NOT NULL OR $%d::text IS NULL)", orgParam, userParam, userParam)
	}
	return fmt.Sprintf(`organization_id = $%d AND (
		COALESCE(visibility, 'organization') <> 'private'
		OR created_by = $%d::text
	)`, orgParam, userParam)
}

func canMutateOwnedResource(ctx context.Context, createdBy string) bool {
	p := auth.PrincipalFromContext(ctx)
	if auth.IsAdminRole(p.Role) {
		return true
	}
	return createdBy != "" && createdBy == p.UserID
}
