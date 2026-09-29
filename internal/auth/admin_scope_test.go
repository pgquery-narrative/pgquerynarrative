package auth

import (
	"net/http/httptest"
	"testing"
)

func TestResolveAdminOrgScope_TenantAdminIgnoresRequestedOrg(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v1/admin/memberships?organization_id=org-b", nil)
	req = req.WithContext(WithPrincipal(req.Context(), Principal{
		UserID: "tenant-admin",
		OrgID:  "org-a",
		Role:   RoleTenantAdmin,
	}))
	rec := httptest.NewRecorder()

	orgID, ok := ResolveAdminOrgScope(rec, req, "org-b")
	if !ok {
		t.Fatal("expected tenant admin own-org scope to be allowed")
	}
	if orgID != "org-a" {
		t.Fatalf("tenant admin must ignore requested org and use principal org, got %q", orgID)
	}
}

func TestResolveAdminOrgScope_TenantAdminOwnOrgAllowed(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v1/admin/memberships", nil)
	req = req.WithContext(WithPrincipal(req.Context(), Principal{
		UserID: "tenant-admin",
		OrgID:  "org-a",
		Role:   RoleTenantAdmin,
	}))
	rec := httptest.NewRecorder()

	orgID, ok := ResolveAdminOrgScope(rec, req, "")
	if !ok {
		t.Fatal("expected tenant admin own-org scope to be allowed")
	}
	if orgID != "org-a" {
		t.Fatalf("expected org-a, got %q", orgID)
	}
}

func TestRequirePlatformAdmin_RejectsTenantAdmin(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v1/admin/organizations", nil)
	req = req.WithContext(WithPrincipal(req.Context(), Principal{
		UserID: "tenant-admin",
		OrgID:  "org-a",
		Role:   RoleTenantAdmin,
	}))
	rec := httptest.NewRecorder()

	if RequirePlatformAdmin(rec, req) {
		t.Fatal("expected tenant admin to be rejected from platform-only admin route")
	}
	if rec.Code != 403 {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestRequireAdmin_AllowsAdminRoles(t *testing.T) {
	for _, role := range []string{RoleTenantAdmin, RolePlatformAdmin} {
		t.Run(role, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/v1/admin/memberships", nil)
			req = req.WithContext(WithPrincipal(req.Context(), Principal{Role: role}))
			rec := httptest.NewRecorder()
			if !RequireAdmin(rec, req) {
				t.Fatalf("expected %s to pass RequireAdmin", role)
			}
		})
	}
}

func TestRequireAdmin_RejectsAnalyst(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v1/admin/memberships", nil)
	req = req.WithContext(WithPrincipal(req.Context(), Principal{Role: RoleAnalyst}))
	rec := httptest.NewRecorder()
	if RequireAdmin(rec, req) {
		t.Fatal("expected analyst to be rejected")
	}
	if rec.Code != 403 {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestResolveAdminOrgScope_RejectsNonAdmin(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v1/admin/memberships", nil)
	req = req.WithContext(WithPrincipal(req.Context(), Principal{Role: RoleAnalyst, OrgID: "org-a"}))
	rec := httptest.NewRecorder()
	if _, ok := ResolveAdminOrgScope(rec, req, "org-a"); ok {
		t.Fatal("expected non-admin to be rejected")
	}
}

func TestRequirePlatformAdmin_AllowsPlatformAdmin(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v1/admin/organizations", nil)
	req = req.WithContext(WithPrincipal(req.Context(), Principal{
		UserID: "platform-admin",
		OrgID:  "org-a",
		Role:   RolePlatformAdmin,
	}))
	rec := httptest.NewRecorder()

	if !RequirePlatformAdmin(rec, req) {
		t.Fatal("expected platform admin to be allowed")
	}
}

func TestResolveAdminOrgScope_PlatformAdminCanCrossOrg(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v1/admin/memberships?organization_id=org-b", nil)
	req = req.WithContext(WithPrincipal(req.Context(), Principal{
		UserID: "platform-admin",
		OrgID:  "org-a",
		Role:   RolePlatformAdmin,
	}))
	rec := httptest.NewRecorder()

	orgID, ok := ResolveAdminOrgScope(rec, req, "org-b")
	if !ok {
		t.Fatal("expected platform admin cross-org scope to be allowed")
	}
	if orgID != "org-b" {
		t.Fatalf("expected org-b, got %q", orgID)
	}
}
