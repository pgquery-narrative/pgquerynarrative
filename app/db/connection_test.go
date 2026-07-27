package db

import (
	"context"
	"testing"

	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/app/config"
)

type stubOrgDSNLookup struct {
	res auth.OrgConnectionResolution
	err error
}

func (s stubOrgDSNLookup) Resolve(context.Context, string, string) (auth.OrgConnectionResolution, error) {
	if s.err != nil {
		return auth.OrgConnectionResolution{}, s.err
	}
	if s.res.Mode == "" {
		return auth.OrgConnectionResolution{Mode: auth.OrgConnectionNoOverride}, nil
	}
	return s.res, nil
}

func TestPoolsAllowedSchemasPrefersTenantOverride(t *testing.T) {
	p := &Pools{
		DefaultConnectionID: "default",
		readonlySpecs: map[string]readonlySpec{
			"default": {opts: poolOptions{searchPath: []string{"demo"}}},
		},
		orgDSN: stubOrgDSNLookup{res: auth.OrgConnectionResolution{
			Mode:    auth.OrgConnectionDedicated,
			Schemas: []string{"tenant_demo"},
		}},
	}
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{OrgID: "org-1"})
	got := p.AllowedSchemas(ctx, "default")
	if len(got) != 1 || got[0] != "tenant_demo" {
		t.Fatalf("expected tenant override, got %v", got)
	}
}

func TestPoolsAllowedSchemasFailClosedModesStillAuthoritative(t *testing.T) {
	p := &Pools{
		DefaultConnectionID: "default",
		readonlySpecs: map[string]readonlySpec{
			"default": {opts: poolOptions{searchPath: []string{"demo"}}},
		},
		orgDSN: stubOrgDSNLookup{res: auth.OrgConnectionResolution{
			Mode:    auth.OrgConnectionKeyUnavailable,
			Schemas: []string{"tenant_only"},
		}},
	}
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{OrgID: "org-1"})
	got := p.AllowedSchemas(ctx, "default")
	if len(got) != 1 || got[0] != "tenant_only" {
		t.Fatalf("expected tenant schemas even when key unavailable, got %v", got)
	}
}

func TestVerifyTenantDSNTLSRequirement(t *testing.T) {
	if err := VerifyTenantDSN(context.Background(), "postgres://u:p@db.example:5432/analytics?sslmode=disable"); err == nil {
		t.Fatal("expected insecure DSN to be rejected")
	}
}

func TestDsnUsesTLS(t *testing.T) {
	cases := map[string]bool{
		"postgres://u:p@db.example:5432/analytics?sslmode=require":          true,
		"postgres://u:p@db.example:5432/analytics?sslmode=verify-full":      true,
		"postgres://u:p@db.example:5432/analytics?sslmode=disable":          false,
		"postgres://u:p@db.example:5432/analytics":                          false,
		"host=db.example port=5432 dbname=analytics user=u sslmode=require": true,
		"host=db.example sslmode=disable":                                   false,
	}
	for dsn, want := range cases {
		if got := dsnUsesTLS(dsn); got != want {
			t.Fatalf("dsnUsesTLS(%q)=%v want %v", dsn, got, want)
		}
	}
}

func TestPoolConfigFallbackSchemasRemainAvailable(t *testing.T) {
	p := &Pools{
		DefaultConnectionID: "default",
		readonlySpecs: map[string]readonlySpec{
			"default": {conn: config.DataConnectionConfig{ID: "default"}, opts: poolOptions{searchPath: []string{"demo"}}},
		},
	}
	got := p.AllowedSchemas(context.Background(), "default")
	if len(got) != 1 || got[0] != "demo" {
		t.Fatalf("expected configured fallback schemas, got %v", got)
	}
}

func TestEnsureOrgReadOnlyPoolFailClosedWithoutKey(t *testing.T) {
	p := &Pools{
		DefaultConnectionID: "default",
		readonlySpecs: map[string]readonlySpec{
			"default": {opts: poolOptions{searchPath: []string{"demo"}}},
		},
		orgDSN: stubOrgDSNLookup{res: auth.OrgConnectionResolution{
			Mode:    auth.OrgConnectionKeyUnavailable,
			Schemas: []string{"tenant_demo"},
		}},
		orgLazy: make(map[string]*lazyReadOnlyPool),
	}
	_, err := p.ensureOrgReadOnlyPool(context.Background(), "org-1", "default")
	if err == nil {
		t.Fatal("expected fail-closed error when encryption key is unavailable")
	}
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{OrgID: "org-1"})
	if pool := p.ReadOnly(ctx, "default"); pool != nil {
		t.Fatal("missing encryption key must not fall back to a shared pool")
	}
}
