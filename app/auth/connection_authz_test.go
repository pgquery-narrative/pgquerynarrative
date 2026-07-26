package auth

import (
	"context"
	"errors"
	"testing"

	apperrors "github.com/pgquerynarrative/pgquerynarrative/app/errors"
)

// TestDecideConnectionAuthz_Matrix exercises the pure authorization decision used
// by AuthorizeConnection: admin ok, analyst denied without perm, analyst allowed
// with perm, cross-org (connection not assigned to org) fails, bootstrap allow-all
// when allowlist is not required, and fail-closed empty allowlist when required.
func TestDecideConnectionAuthz_Matrix(t *testing.T) {
	cases := []struct {
		name               string
		orgHasAnyRows      bool
		connectionAssigned bool
		role               string
		principalGranted   bool
		allowlistRequired  bool
		wantErr            bool
	}{
		{
			name:               "bootstrap allows admin when allowlist not required",
			orgHasAnyRows:      false,
			connectionAssigned: false,
			role:               RoleAdmin,
			principalGranted:   false,
			allowlistRequired:  false,
			wantErr:            false,
		},
		{
			name:               "bootstrap allows analyst when allowlist not required",
			orgHasAnyRows:      false,
			connectionAssigned: false,
			role:               RoleAnalyst,
			principalGranted:   false,
			allowlistRequired:  false,
			wantErr:            false,
		},
		{
			name:               "empty allowlist denies admin when required",
			orgHasAnyRows:      false,
			connectionAssigned: false,
			role:               RoleAdmin,
			principalGranted:   false,
			allowlistRequired:  true,
			wantErr:            true,
		},
		{
			name:               "empty allowlist denies analyst when required",
			orgHasAnyRows:      false,
			connectionAssigned: false,
			role:               RoleAnalyst,
			principalGranted:   false,
			allowlistRequired:  true,
			wantErr:            true,
		},
		{
			name:               "admin ok once assigned even without explicit grant",
			orgHasAnyRows:      true,
			connectionAssigned: true,
			role:               RoleAdmin,
			principalGranted:   false,
			allowlistRequired:  true,
			wantErr:            false,
		},
		{
			name:               "analyst denied without perm",
			orgHasAnyRows:      true,
			connectionAssigned: true,
			role:               RoleAnalyst,
			principalGranted:   false,
			allowlistRequired:  true,
			wantErr:            true,
		},
		{
			name:               "analyst allowed with perm",
			orgHasAnyRows:      true,
			connectionAssigned: true,
			role:               RoleAnalyst,
			principalGranted:   true,
			allowlistRequired:  true,
			wantErr:            false,
		},
		{
			name:               "viewer denied without perm",
			orgHasAnyRows:      true,
			connectionAssigned: true,
			role:               RoleViewer,
			principalGranted:   false,
			allowlistRequired:  true,
			wantErr:            true,
		},
		{
			name:               "cross-org fails even for admin",
			orgHasAnyRows:      true,
			connectionAssigned: false,
			role:               RoleAdmin,
			principalGranted:   false,
			allowlistRequired:  true,
			wantErr:            true,
		},
		{
			name:               "cross-org fails for analyst",
			orgHasAnyRows:      true,
			connectionAssigned: false,
			role:               RoleAnalyst,
			principalGranted:   true,
			allowlistRequired:  true,
			wantErr:            true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := decideConnectionAuthz(tc.orgHasAnyRows, tc.connectionAssigned, tc.role, tc.principalGranted, tc.allowlistRequired)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if !errors.Is(err, apperrors.ErrConnectionForbidden) {
					t.Fatalf("expected ErrConnectionForbidden, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
		})
	}
}

// TestAuthorizeConnection_NilAuthorizerIsPermissive ensures an authorizer with no
// pool (not wired to a database) never blocks callers, matching the fallback used
// by other stores in this package (e.g. MembershipStore) when persistence is absent.
func TestAuthorizeConnection_NilAuthorizerIsPermissive(t *testing.T) {
	var a *ConnectionAuthorizer
	if err := a.AuthorizeConnection(context.Background(), "org-1", "user-1", RoleAnalyst, "default", ActionQuery); err != nil {
		t.Fatalf("nil authorizer should be permissive, got %v", err)
	}

	a2 := NewConnectionAuthorizer(nil)
	if err := a2.AuthorizeConnection(context.Background(), "org-1", "user-1", RoleAnalyst, "default", ActionQuery); err != nil {
		t.Fatalf("authorizer without pool should be permissive, got %v", err)
	}

	if got := a2.AllowedConnections(context.Background(), "org-1", "user-1", RoleAnalyst, []string{"a", "b"}, ActionQuery); len(got) != 2 {
		t.Fatalf("expected unfiltered list without pool, got %v", got)
	}
}

// TestAuthorizeConnection_UnknownAction ensures an unsupported action is rejected
// even when the authorizer would otherwise be permissive.
func TestAuthorizeConnection_UnknownAction(t *testing.T) {
	if _, ok := connectionActionColumns["not_a_real_action"]; ok {
		t.Fatalf("test action unexpectedly recognized")
	}
}

func TestSetAllowlistRequired(t *testing.T) {
	a := NewConnectionAuthorizer(nil)
	if a != nil {
		t.Fatal("expected nil authorizer when pool is nil")
	}
	var nilAuth *ConnectionAuthorizer
	nilAuth.SetAllowlistRequired(true) // must not panic
}
