package service

import (
	"context"
	"strings"
	"testing"

	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
)

func TestVisibleResourcePredicate(t *testing.T) {
	t.Run("admins see every row in their organization", func(t *testing.T) {
		for _, role := range []string{auth.RolePlatformAdmin, auth.RoleTenantAdmin} {
			got := visibleResourcePredicate(1, 2, role)
			if !strings.Contains(got, "organization_id = $1") {
				t.Errorf("%s: organization scope missing: %q", role, got)
			}
			if strings.Contains(got, "created_by") || strings.Contains(got, "visibility") {
				t.Errorf("%s: admins must not be narrowed by ownership or visibility: %q", role, got)
			}
			// The user parameter is always bound by the caller, so it must still be
			// referenced (with a cast) or PostgreSQL raises 42P18 on the unused $2.
			if !strings.Contains(got, "$2::text") {
				t.Errorf("%s: user param must stay referenced with an explicit cast: %q", role, got)
			}
		}
	})

	t.Run("non-admins are limited to shared rows plus their own private ones", func(t *testing.T) {
		for _, role := range []string{auth.RoleAnalyst, auth.RoleViewer, "", "something-unknown"} {
			got := visibleResourcePredicate(1, 2, role)
			if !strings.Contains(got, "organization_id = $1") {
				t.Errorf("%q: organization scope missing: %q", role, got)
			}
			if !strings.Contains(got, "created_by = $2::text") {
				t.Errorf("%q: own-row escape hatch missing: %q", role, got)
			}
			if !strings.Contains(got, "COALESCE(visibility, 'organization') <> 'private'") {
				t.Errorf("%q: private rows must be excluded by default: %q", role, got)
			}
		}
	})

	t.Run("parameter indexes are honoured", func(t *testing.T) {
		got := visibleResourcePredicate(4, 7, auth.RoleAnalyst)
		if !strings.Contains(got, "$4") || !strings.Contains(got, "$7::text") {
			t.Errorf("expected $4/$7, got %q", got)
		}
	})
}

func TestCanMutateOwnedResource(t *testing.T) {
	ctxAs := func(role, userID string) context.Context {
		return auth.WithPrincipal(context.Background(), auth.Principal{
			UserID: userID, OrgID: auth.DefaultOrganizationID, Role: role,
		})
	}

	t.Run("admins may mutate any row, including unowned ones", func(t *testing.T) {
		for _, role := range []string{auth.RolePlatformAdmin, auth.RoleTenantAdmin} {
			if !canMutateOwnedResource(ctxAs(role, "admin-1"), "someone-else") {
				t.Errorf("%s should be able to mutate another user's row", role)
			}
			// Rows created before created_by was populated are still admin-deletable.
			if !canMutateOwnedResource(ctxAs(role, "admin-1"), "") {
				t.Errorf("%s should be able to mutate a row with no recorded creator", role)
			}
		}
	})

	t.Run("non-admins may mutate only their own rows", func(t *testing.T) {
		if !canMutateOwnedResource(ctxAs(auth.RoleAnalyst, "user-1"), "user-1") {
			t.Error("the creator should be able to mutate their own row")
		}
		if canMutateOwnedResource(ctxAs(auth.RoleAnalyst, "user-1"), "user-2") {
			t.Error("an analyst must not mutate another user's row")
		}
		if canMutateOwnedResource(ctxAs(auth.RoleViewer, "user-1"), "user-1") == false {
			t.Error("a viewer's own row is still theirs")
		}
	})

	t.Run("a blank creator is never mutable by a non-admin", func(t *testing.T) {
		// Guards against "" == "" matching an unauthenticated principal.
		if canMutateOwnedResource(ctxAs(auth.RoleAnalyst, ""), "") {
			t.Error("a blank creator must not match a blank user id")
		}
	})
}
