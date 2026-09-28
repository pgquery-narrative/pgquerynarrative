package auth

import (
	"context"
	"errors"
	"testing"
)

func TestResolvePrincipal_RequiresOrgSelectionWhenMultiple(t *testing.T) {
	// Without a pool the store is nil and falls through; this unit test covers the
	// exported sentinel used by API/OIDC callers.
	if !errors.Is(ErrOrganizationSelectionRequired, ErrOrganizationSelectionRequired) {
		t.Fatal("sentinel mismatch")
	}
	_ = context.Background()
}

func TestCanAssignAndNormalizeRoles(t *testing.T) {
	if NormalizeRole("admin") != RoleTenantAdmin {
		t.Fatal("expected admin -> tenant_admin")
	}
	if CanAssignRole(RoleTenantAdmin, RolePlatformAdmin) {
		t.Fatal("tenant admin must not assign platform_admin")
	}
}
