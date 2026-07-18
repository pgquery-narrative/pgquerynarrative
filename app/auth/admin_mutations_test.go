package auth

import "testing"

func TestHashAPIKeyStable(t *testing.T) {
	a := HashAPIKey("pgqn_test")
	b := HashAPIKey("pgqn_test")
	if a == "" || a != b || a == HashAPIKey("other") {
		t.Fatalf("hash instability: %q %q", a, b)
	}
}

func TestDecideConnectionAuthzMatrix(t *testing.T) {
	cases := []struct {
		name                         string
		orgHasAny, assigned, granted bool
		role                         string
		wantErr                      bool
	}{
		{"bootstrap", false, false, false, RoleAnalyst, false},
		{"not_assigned", true, false, false, RoleAnalyst, true},
		{"admin_ok", true, true, false, RoleAdmin, false},
		{"granted", true, true, true, RoleAnalyst, false},
		{"denied", true, true, false, RoleAnalyst, true},
	}
	for _, tc := range cases {
		err := decideConnectionAuthz(tc.orgHasAny, tc.assigned, tc.role, tc.granted)
		if (err != nil) != tc.wantErr {
			t.Fatalf("%s: err=%v wantErr=%v", tc.name, err, tc.wantErr)
		}
	}
}
