package service

import (
	"context"
	"testing"
	"time"

	"github.com/pgquerynarrative/pgquerynarrative/api/gen/workspace"
	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/app/config"
	"github.com/pgquerynarrative/pgquerynarrative/app/queryrunner"
)

// mockConnectionAuthorizer grants only the actions listed for a connection,
// so tests can assert authorization_state reflects a real decision instead
// of a hardcoded default.
type mockConnectionAuthorizer struct {
	granted map[string]map[string]bool // connectionID -> action -> allowed
}

func (m *mockConnectionAuthorizer) AuthorizeConnection(_ context.Context, _, _, _, connectionID, action string) error {
	if m.granted[connectionID][action] {
		return nil
	}
	return auth.ErrConnectionForbidden
}

func (m *mockConnectionAuthorizer) AllowedConnections(_ context.Context, _, _, _ string, configured []string, _ string) []string {
	return configured
}

func testRunner(maxRows int, timeout time.Duration) *queryrunner.Runner {
	validator := queryrunner.NewValidator([]string{"demo"}, 10000)
	return queryrunner.NewRunner(nil, validator, maxRows, timeout)
}

func TestSecurityTrust_ReportsRawSSLModeNeverSubstituted(t *testing.T) {
	qs := NewQueriesServiceMultiConnection(nil,
		map[string]*queryrunner.Runner{"default": testRunner(500, 0)},
		"default", config.MetricsConfig{}, nil, nil, "", nil)
	s := NewWorkspaceService(nil, qs, true, false, false, "off", false,
		[]config.DataConnectionConfig{{ID: "default", SSLMode: "disable", AllowedSchemas: nil}},
		"default")

	got, err := s.SecurityTrust(context.Background(), &workspace.SecurityTrustPayload{})
	if err != nil {
		t.Fatalf("SecurityTrust: %v", err)
	}
	if got.TLS != "disable" {
		t.Fatalf("tls: got %q, want the raw configured value %q (must never be upgraded to a friendlier mode)", got.TLS, "disable")
	}
	if got.QueryTimeoutSeconds != 0 {
		t.Fatalf("query_timeout_seconds: got %d, want 0 (a real unlimited timeout must not be hidden behind a synthetic default)", got.QueryTimeoutSeconds)
	}
	if len(got.AllowedSchemas) != 0 {
		t.Fatalf("allowed_schemas: got %v, want empty (must not be silently substituted with a hardcoded default)", got.AllowedSchemas)
	}
	if got.ConnectionID != "default" {
		t.Fatalf("connection_id: got %q, want %q", got.ConnectionID, "default")
	}
	if got.Readonly {
		t.Fatal("readonly: probe has no live pool, must fail closed to false, not assume safety")
	}
}

func TestSecurityTrust_SelectsRequestedConnection(t *testing.T) {
	qs := NewQueriesServiceMultiConnection(nil,
		map[string]*queryrunner.Runner{
			"default":   testRunner(500, 5*time.Second),
			"analytics": testRunner(2000, 30*time.Second),
		},
		"default", config.MetricsConfig{}, nil, nil, "", nil)
	s := NewWorkspaceService(nil, qs, true, false, true, "off", false,
		[]config.DataConnectionConfig{
			{ID: "default", SSLMode: "disable", AllowedSchemas: []string{"demo"}},
			{ID: "analytics", SSLMode: "require", AllowedSchemas: []string{"analytics"}},
		},
		"default")

	connID := "analytics"
	got, err := s.SecurityTrust(context.Background(), &workspace.SecurityTrustPayload{ConnectionID: &connID})
	if err != nil {
		t.Fatalf("SecurityTrust: %v", err)
	}
	if got.ConnectionID != "analytics" || got.TLS != "require" {
		t.Fatalf("got connection_id=%q tls=%q, want analytics/require", got.ConnectionID, got.TLS)
	}
	if got.QueryTimeoutSeconds != 30 || got.ResultLimit != 2000 {
		t.Fatalf("got timeout=%d limit=%d, want the analytics runner's real values (30, 2000)", got.QueryTimeoutSeconds, got.ResultLimit)
	}

	unknown := "does-not-exist"
	if _, err := s.SecurityTrust(context.Background(), &workspace.SecurityTrustPayload{ConnectionID: &unknown}); err == nil {
		t.Fatal("expected an error for an unknown connection_id, not a silent fallback")
	}
}

func TestSecurityTrust_AuthorizationStateReflectsRealGrants(t *testing.T) {
	qs := NewQueriesServiceMultiConnection(nil,
		map[string]*queryrunner.Runner{"default": testRunner(500, 0)},
		"default", config.MetricsConfig{}, nil, nil, "", nil)
	qs.SetAuthorizer(&mockConnectionAuthorizer{granted: map[string]map[string]bool{
		"default": {auth.ActionQuery: true, auth.ActionExplain: true},
	}})
	s := NewWorkspaceService(nil, qs, true, false, true, "off", false,
		[]config.DataConnectionConfig{{ID: "default", SSLMode: "require", AllowedSchemas: []string{"demo"}}},
		"default")

	got, err := s.SecurityTrust(context.Background(), &workspace.SecurityTrustPayload{})
	if err != nil {
		t.Fatalf("SecurityTrust: %v", err)
	}
	if len(got.AuthorizationState) != 2 {
		t.Fatalf("authorization_state: got %v, want exactly the two granted actions", got.AuthorizationState)
	}
	for _, a := range got.AuthorizationState {
		if a != auth.ActionQuery && a != auth.ActionExplain {
			t.Fatalf("authorization_state contains ungranted action %q", a)
		}
	}
	if got.AnalyzePolicy != "Disabled (no permission)" {
		t.Fatalf("analyze_policy: got %q, want %q since analyze was not granted", got.AnalyzePolicy, "Disabled (no permission)")
	}
}

func TestSecurityTrust_AnalyzePolicyDisabledByServerRegardlessOfPermission(t *testing.T) {
	qs := NewQueriesServiceMultiConnection(nil,
		map[string]*queryrunner.Runner{"default": testRunner(500, 0)},
		"default", config.MetricsConfig{}, nil, nil, "", nil)
	s := NewWorkspaceService(nil, qs, true, false, false /* explain analyze disabled server-wide */, "off", false,
		[]config.DataConnectionConfig{{ID: "default", SSLMode: "require", AllowedSchemas: []string{"demo"}}},
		"default")

	got, err := s.SecurityTrust(context.Background(), &workspace.SecurityTrustPayload{})
	if err != nil {
		t.Fatalf("SecurityTrust: %v", err)
	}
	if got.AnalyzePolicy != "Disabled (server policy)" {
		t.Fatalf("analyze_policy: got %q, want %q", got.AnalyzePolicy, "Disabled (server policy)")
	}
	if got.ExplainAnalyze != "Disabled" {
		t.Fatalf("explain_analyze: got %q, want Disabled", got.ExplainAnalyze)
	}
	if got.LastSecurityVerification != nil {
		t.Fatalf("last_security_verification: got %v, want nil — no verification marker is persisted", got.LastSecurityVerification)
	}
}
