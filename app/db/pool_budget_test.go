package db

import (
	"testing"
	"time"
)

func TestReserveOrgPoolLRUEviction(t *testing.T) {
	p := &Pools{
		orgLazy:     make(map[string]*lazyReadOnlyPool),
		maxOrgPools: 2,
	}
	p.orgLazy["a\x00default"] = &lazyReadOnlyPool{lastUsed: time.Now().Add(-2 * time.Hour)}
	p.orgLazy["b\x00default"] = &lazyReadOnlyPool{lastUsed: time.Now().Add(-1 * time.Hour)}
	if err := p.reserveOrgPoolLocked(); err != nil {
		t.Fatal(err)
	}
	if len(p.orgLazy) != 1 {
		t.Fatalf("expected one pool after eviction, got %d", len(p.orgLazy))
	}
	if _, ok := p.orgLazy["b\x00default"]; !ok {
		t.Fatal("expected newer pool to be retained")
	}
	if p.OrgPoolEvictions() != 1 {
		t.Fatalf("expected one eviction, got %d", p.OrgPoolEvictions())
	}
}

func TestTenantPoolMetricNameBounded(t *testing.T) {
	name := tenantPoolMetricName("00000000-0000-0000-0000-000000000001\x00default")
	if name == "" || len(name) > 64 {
		t.Fatalf("unexpected metric name %q", name)
	}
}
