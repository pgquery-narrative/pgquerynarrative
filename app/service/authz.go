package service

import (
	"context"

	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
)

// checkConnectionAccess authorizes the current request principal for action on
// connectionID, using the org/user/role carried on ctx (see auth.PrincipalFromContext).
// A nil authorizer (service not wired with one, e.g. many existing unit tests that
// construct services directly) is permissive so existing behavior is unaffected.
//
// Callers must run this BEFORE resolving a runner/loader for connectionID so that a
// denied principal never gets a live connection handle (see individual call sites in
// queries.go, reports_generate.go, schema.go, ask.go, and schedules.go).
func checkConnectionAccess(ctx context.Context, authz ConnectionAuthorizer, connectionID, action string) error {
	if authz == nil {
		return nil
	}
	p := auth.PrincipalFromContext(ctx)
	return authz.AuthorizeConnection(ctx, p.OrgID, p.UserID, p.Role, connectionID, action)
}
