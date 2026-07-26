package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/pgquerynarrative/pgquerynarrative/app/security"
)

func TestOrgConnectionSecretSealRoundTrip(t *testing.T) {
	key := []byte("unit-test-encryption-key-32bytes!!")
	plain := "postgres://ro:secret@db.example:5432/analytics?sslmode=require"
	sealed, err := security.Seal(key, plain)
	if err != nil {
		t.Fatal(err)
	}
	if sealed == plain {
		t.Fatal("expected sealed envelope, got plaintext")
	}
	out, err := security.Open(key, sealed)
	if err != nil {
		t.Fatal(err)
	}
	if out != plain {
		t.Fatalf("round-trip mismatch: %q", out)
	}
}

func TestNewOrgConnectionSecretStoreNilPool(t *testing.T) {
	if NewOrgConnectionSecretStore(nil, "key") != nil {
		t.Fatal("expected nil store without pool")
	}
}

func TestOrgConnectionSecretStore_OpenDSNNilStore(t *testing.T) {
	var store *OrgConnectionSecretStore
	dsn, schemas, ok, err := store.OpenDSN(context.Background(), DefaultOrganizationID, "default")
	if err != nil || ok || dsn != "" || schemas != nil {
		t.Fatalf("nil store: dsn=%q ok=%v err=%v schemas=%v", dsn, ok, err, schemas)
	}
}

func TestOrgConnectionSecretStore_MutationsRequireConfig(t *testing.T) {
	var store *OrgConnectionSecretStore
	if err := store.Upsert(context.Background(), DefaultOrganizationID, "default", "postgres://x", nil); err == nil {
		t.Fatal("expected upsert error on nil store")
	}
	if err := store.Delete(context.Background(), DefaultOrganizationID, "default"); err == nil {
		t.Fatal("expected delete error on nil store")
	}
	if _, err := store.List(context.Background(), DefaultOrganizationID); err == nil {
		t.Fatal("expected list error on nil store")
	}

	store = &OrgConnectionSecretStore{} // pool nil, empty key
	if err := store.Upsert(context.Background(), DefaultOrganizationID, "default", "postgres://x", nil); err == nil {
		t.Fatal("expected upsert error without pool")
	}
}

func TestAllowlistRequiredToggle(t *testing.T) {
	if NewConnectionAuthorizer(nil) != nil {
		t.Fatal("expected nil authorizer without pool")
	}
	a := &ConnectionAuthorizer{}
	if a.AllowlistRequired() {
		t.Fatal("default allowlist required should be false")
	}
	a.SetAllowlistRequired(true)
	if !a.AllowlistRequired() {
		t.Fatal("expected allowlist required after SetAllowlistRequired(true)")
	}
	var nilAuth *ConnectionAuthorizer
	nilAuth.SetAllowlistRequired(true)
	if nilAuth.AllowlistRequired() {
		t.Fatal("nil authorizer AllowlistRequired should be false")
	}
}

func TestMembershipRequiredEnv(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("SECURITY_STRICT", "")
	if membershipRequired() {
		t.Fatal("dev env should not require membership")
	}
	t.Setenv("APP_ENV", "production")
	if !membershipRequired() {
		t.Fatal("production should require membership")
	}
	t.Setenv("APP_ENV", "dev")
	t.Setenv("SECURITY_STRICT", "true")
	if !membershipRequired() {
		t.Fatal("SECURITY_STRICT=true should require membership")
	}
}

func TestMembershipStore_NilFallsBackOutsideStrict(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("SECURITY_STRICT", "")
	var s *MembershipStore
	p, err := s.ResolvePrincipal(context.Background(), "user-1", "", RoleAnalyst)
	if err != nil {
		t.Fatal(err)
	}
	if p.OrgID != DefaultOrganizationID || p.UserID != "user-1" || p.Role != RoleAnalyst {
		t.Fatalf("unexpected principal: %+v", p)
	}
	p, err = s.ResolveFromGroupClaims(context.Background(), "user-2", "", RoleViewer, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.OrgID != DefaultOrganizationID || p.Role != RoleViewer {
		t.Fatalf("unexpected principal from groups: %+v", p)
	}
}

func TestMembershipStore_NilFailsClosedInProduction(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	var s *MembershipStore
	if _, err := s.ResolvePrincipal(context.Background(), "user-1", "", RoleAnalyst); !errors.Is(err, ErrNoOrganizationMembership) {
		t.Fatalf("expected ErrNoOrganizationMembership, got %v", err)
	}
	if NewMembershipStore(nil, false) != nil {
		t.Fatal("expected nil store without pool")
	}
}

func TestAssignConnectionNilAuthorizer(t *testing.T) {
	var a *ConnectionAuthorizer
	if err := a.AssignConnection(context.Background(), DefaultOrganizationID, "default"); err == nil {
		t.Fatal("expected error on nil authorizer")
	}
	if err := a.UnassignConnection(context.Background(), DefaultOrganizationID, "default"); err == nil {
		t.Fatal("expected error on nil authorizer")
	}
	if _, err := a.ListAssignedConnections(context.Background(), DefaultOrganizationID); err == nil {
		t.Fatal("expected error on nil authorizer")
	}
	if err := a.GrantPermission(context.Background(), DefaultOrganizationID, "default", "user-1", map[string]bool{"query": true}); err == nil {
		t.Fatal("expected grant error on nil authorizer")
	}
	if err := a.RevokePermission(context.Background(), DefaultOrganizationID, "default", "user-1"); err == nil {
		t.Fatal("expected revoke error on nil authorizer")
	}
}

func TestAllowedConnectionsNilAuthorizer(t *testing.T) {
	var a *ConnectionAuthorizer
	got := a.AllowedConnections(context.Background(), DefaultOrganizationID, "u", RoleAdmin, []string{"default", "other"}, ActionQuery)
	if len(got) != 2 || got[0] != "default" || got[1] != "other" {
		t.Fatalf("nil authorizer should pass through configured ids, got %#v", got)
	}
	list, err := a.ListAllowedConnections(context.Background(), DefaultOrganizationID, "u", RoleAdmin, []string{"default"}, ActionQuery)
	if err != nil || len(list) != 1 || list[0] != "default" {
		t.Fatalf("ListAllowedConnections: list=%v err=%v", list, err)
	}
}
