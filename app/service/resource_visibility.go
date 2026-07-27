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
		return fmt.Sprintf("organization_id = $%d", orgParam)
	}
	return fmt.Sprintf(`organization_id = $%d AND (
		COALESCE(visibility, 'organization') <> 'private'
		OR created_by = $%d
	)`, orgParam, userParam)
}

func canMutateOwnedResource(ctx context.Context, createdBy string) bool {
	p := auth.PrincipalFromContext(ctx)
	if auth.IsAdminRole(p.Role) {
		return true
	}
	return createdBy != "" && createdBy == p.UserID
}
