package db

import (
	"context"
	"errors"
	"testing"

	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
)

type fakeDSNLookup struct {
	res auth.OrgConnectionResolution
	err error
}

func (f fakeDSNLookup) Resolve(context.Context, string, string) (auth.OrgConnectionResolution, error) {
	return f.res, f.err
}

func TestSharedReadOnlyRole(t *testing.T) {
	inOrg := auth.WithPrincipal(context.Background(), auth.Principal{UserID: "u", OrgID: "org-a", Role: auth.RoleViewer})
	for _, tc := range []struct {
		name   string
		pools  *Pools
		ctx    context.Context
		shared bool
	}{
		{"no org lookup configured", &Pools{DefaultConnectionID: "default"}, inOrg, true},
		{"no organization in the request", &Pools{orgDSN: fakeDSNLookup{res: auth.OrgConnectionResolution{Mode: auth.OrgConnectionDedicated}}}, context.Background(), true},
		{"organization without credentials", &Pools{orgDSN: fakeDSNLookup{res: auth.OrgConnectionResolution{Mode: auth.OrgConnectionNoOverride}}}, inOrg, true},
		{"lookup failed: counts as shared", &Pools{orgDSN: fakeDSNLookup{err: errors.New("boom")}}, inOrg, true},
		{"organization with its own credentials", &Pools{orgDSN: fakeDSNLookup{res: auth.OrgConnectionResolution{Mode: auth.OrgConnectionDedicated}}}, inOrg, false},
	} {
		if got := tc.pools.SharedReadOnlyRole(tc.ctx, "default"); got != tc.shared {
			t.Errorf("%s: shared = %v, want %v", tc.name, got, tc.shared)
		}
	}
}
