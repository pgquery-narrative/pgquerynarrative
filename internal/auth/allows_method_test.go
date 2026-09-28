package auth

import (
	"net/http"
	"testing"
)

func TestAllowsMethod_AnalystSavedQueries(t *testing.T) {
	if !AllowsMethod(RoleAnalyst, http.MethodPost, "/api/v1/queries/saved") {
		t.Fatal("analyst should be able to save queries")
	}
	if !AllowsMethod(RoleAnalyst, http.MethodDelete, "/api/v1/queries/saved/abc") {
		t.Fatal("analyst should be able to delete saved queries")
	}
	if !AllowsMethod(RoleAnalyst, http.MethodPost, "/api/v1/schedules") {
		t.Fatal("analyst should be able to create schedules")
	}
	if !AllowsMethod(RoleAnalyst, http.MethodPost, "/api/v1/schedules/abc/run") {
		t.Fatal("analyst should be able to run schedules")
	}
	if AllowsMethod(RoleViewer, http.MethodPost, "/api/v1/queries/saved") {
		t.Fatal("viewer must not save queries")
	}
}

// Registered routes that sit directly next to already-allowed analyst
// capabilities (comparing explain plans next to running one; retrying a
// schedule run next to running a schedule) must not fall through the
// allowlist and 403 by accident.
func TestAllowsMethod_AnalystNeighboringRoutes(t *testing.T) {
	if !AllowsMethod(RoleAnalyst, http.MethodPost, "/api/v1/queries/explain/compare") {
		t.Fatal("analyst should be able to compare explain plans")
	}
	if !AllowsMethod(RoleAnalyst, http.MethodPost, "/api/v1/schedule-runs/abc/retry") {
		t.Fatal("analyst should be able to retry a schedule run")
	}
	if AllowsMethod(RoleViewer, http.MethodPost, "/api/v1/queries/explain/compare") {
		t.Fatal("viewer must not compare explain plans")
	}
	if AllowsMethod(RoleViewer, http.MethodPost, "/api/v1/schedule-runs/abc/retry") {
		t.Fatal("viewer must not retry a schedule run")
	}
}

func TestAllowsMethod_AnalystInvestigations(t *testing.T) {
	paths := []string{
		"/api/v1/investigations",
		"/api/v1/investigations/from-regression",
		"/api/v1/investigations/abc/candidate",
		"/api/v1/investigations/abc/suggest-rewrite",
		"/api/v1/investigations/abc/rank-candidates",
		"/api/v1/investigations/abc/report",
		"/api/v1/workspace/regressions/abc/acknowledge",
	}
	for _, path := range paths {
		if !AllowsMethod(RoleAnalyst, http.MethodPost, path) {
			t.Fatalf("analyst should write %s", path)
		}
	}
	if AllowsMethod(RoleAnalyst, http.MethodPost, "/api/v1/dashboards") {
		t.Fatal("analyst must not create dashboards without explicit grant")
	}
	if AllowsMethod(RoleViewer, http.MethodPost, "/api/v1/investigations") {
		t.Fatal("viewer must not create investigations")
	}
}
