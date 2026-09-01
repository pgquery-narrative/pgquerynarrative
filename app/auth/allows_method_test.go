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
