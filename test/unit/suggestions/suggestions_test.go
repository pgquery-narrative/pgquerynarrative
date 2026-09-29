package suggestions_test

import (
	"context"
	"testing"

	suggestionsgen "github.com/pgquerynarrative/pgquerynarrative/api/gen/suggestions"
	"github.com/pgquerynarrative/pgquerynarrative/internal/suggestions"
)

func TestQueries_EmptyIntent_ReturnsCuratedOnly(t *testing.T) {
	// Suggester with nil pool: when intent is empty we never query DB, so this is safe.
	suggester := suggestions.NewSuggester(nil)
	ctx := context.Background()
	payload := &suggestionsgen.QueriesPayload{Limit: 5}

	res, err := suggester.Queries(ctx, payload)
	if err != nil {
		t.Fatalf("Queries: %v", err)
	}
	if res == nil || len(res.Suggestions) == 0 {
		t.Fatal("expected at least one curated suggestion")
	}
	for i, s := range res.Suggestions {
		if s.Source != "curated" {
			t.Errorf("suggestion %d: expected source curated, got %q", i, s.Source)
		}
		if s.SQL == "" || s.Title == "" {
			t.Errorf("suggestion %d: sql and title must be set", i)
		}
	}
	// We have exactly 3 curated items
	if len(res.Suggestions) != 3 {
		t.Errorf("expected 3 curated suggestions, got %d", len(res.Suggestions))
	}
}

func TestQueries_RespectsLimit(t *testing.T) {
	suggester := suggestions.NewSuggester(nil)
	ctx := context.Background()
	payload := &suggestionsgen.QueriesPayload{Limit: 2}

	res, err := suggester.Queries(ctx, payload)
	if err != nil {
		t.Fatalf("Queries: %v", err)
	}
	if len(res.Suggestions) != 2 {
		t.Errorf("expected 2 suggestions (limit=2), got %d", len(res.Suggestions))
	}
}
