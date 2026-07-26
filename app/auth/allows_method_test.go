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
