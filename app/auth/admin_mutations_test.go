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
		allowlistRequired            bool
		wantErr                      bool
	}{
		{"bootstrap", false, false, false, RoleAnalyst, false, false},
		{"empty_deny", false, false, false, RoleAnalyst, true, true},
		{"not_assigned", true, false, false, RoleAnalyst, true, true},
		{"admin_ok", true, true, false, RoleAdmin, true, false},
		{"granted", true, true, true, RoleAnalyst, true, false},
		{"denied", true, true, false, RoleAnalyst, true, true},
	}
	for _, tc := range cases {
		err := decideConnectionAuthz(tc.orgHasAny, tc.assigned, tc.role, tc.granted, tc.allowlistRequired)
		if (err != nil) != tc.wantErr {
			t.Fatalf("%s: err=%v wantErr=%v", tc.name, err, tc.wantErr)
		}
	}
}
