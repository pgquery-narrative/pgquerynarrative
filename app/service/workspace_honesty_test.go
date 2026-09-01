package service

import (
	"context"
	"testing"

	"github.com/pgquerynarrative/pgquerynarrative/app/config"
)

func TestSeedDemoRegressions_GatedByDemoMode(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	if config.DemoMode() {
		t.Fatal("expected non-demo")
	}
	s := &WorkspaceService{} // pool unused when gated off
	if err := s.seedDemoRegressions(context.Background(), "00000000-0000-0000-0000-000000000001"); err != nil {
		t.Fatalf("non-demo seed must no-op: %v", err)
	}
}

func TestDemoScenarios_NoAnswerKeyCandidate(t *testing.T) {
	s := &WorkspaceService{}
	list, err := s.DemoScenarios(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if list == nil || len(list.Items) == 0 {
		t.Fatal("expected scenarios")
	}
	for _, item := range list.Items {
		if item.CandidateSQL != nil && *item.CandidateSQL != "" {
			t.Fatalf("scenario %q must not ship answer-key candidate_sql; got %q", item.ID, *item.CandidateSQL)
		}
		if item.SQL == "" {
			t.Fatalf("scenario %q needs problem SQL", item.ID)
		}
	}
}
