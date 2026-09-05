package service

import (
	"strconv"
	"strings"
	"testing"

	reports "github.com/pgquerynarrative/pgquerynarrative/api/gen/reports"
	suggestions "github.com/pgquerynarrative/pgquerynarrative/api/gen/suggestions"
)

func TestParseSuggestedQuestions(t *testing.T) {
	t.Run("strips list markers and numbering", func(t *testing.T) {
		raw := "- What are the top products?\n1. How did revenue trend?\n2) Which regions grew?\n"
		got := parseSuggestedQuestions(raw, 10)
		want := []string{"What are the top products?", "How did revenue trend?", "Which regions grew?"}
		if len(got) != len(want) {
			t.Fatalf("got %d questions (%v), want %d", len(got), got, len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("question %d = %q, want %q", i, got[i], want[i])
			}
		}
	})

	t.Run("appends a missing question mark", func(t *testing.T) {
		got := parseSuggestedQuestions("Show revenue by region", 5)
		if len(got) != 1 || got[0] != "Show revenue by region?" {
			t.Errorf("got %v", got)
		}
	})

	t.Run("deduplicates case-insensitively", func(t *testing.T) {
		got := parseSuggestedQuestions("Top products?\ntop PRODUCTS?\n- Top Products", 10)
		if len(got) != 1 {
			t.Errorf("expected 1 unique question, got %v", got)
		}
	})

	t.Run("honours the limit", func(t *testing.T) {
		var b strings.Builder
		for i := 0; i < 20; i++ {
			b.WriteString("Question " + strconv.Itoa(i) + "?\n")
		}
		got := parseSuggestedQuestions(b.String(), 3)
		if len(got) != 3 {
			t.Errorf("expected 3 questions, got %d", len(got))
		}
	})

	t.Run("blank lines and empty input yield nothing", func(t *testing.T) {
		if got := parseSuggestedQuestions("\n\n   \n", 5); len(got) != 0 {
			t.Errorf("expected no questions, got %v", got)
		}
		if got := parseSuggestedQuestions("", 5); len(got) != 0 {
			t.Errorf("expected no questions, got %v", got)
		}
	})
}

func TestBuildQuestionDiscoveryPrompt(t *testing.T) {
	schema := "CREATE TABLE demo.sales (date date, region text, revenue numeric);"
	got := buildQuestionDiscoveryPrompt(schema, 6)
	if !strings.Contains(got, schema) {
		t.Error("the schema must be included in the prompt")
	}
	if !strings.Contains(got, "exactly 6") {
		t.Errorf("the limit must be stated in the prompt: %q", got)
	}
	if !strings.Contains(got, "no SQL") {
		t.Error("the prompt must constrain the model to natural language")
	}
}

func TestDefaultQuestionsAndFollowUps(t *testing.T) {
	t.Run("default questions are non-empty and unique", func(t *testing.T) {
		qs := defaultQuestions()
		if len(qs) == 0 {
			t.Fatal("expected fallback questions")
		}
		seen := map[string]bool{}
		for _, q := range qs {
			if strings.TrimSpace(q) == "" {
				t.Error("blank fallback question")
			}
			if !strings.HasSuffix(q, "?") {
				t.Errorf("fallback question is not a question: %q", q)
			}
			if seen[q] {
				t.Errorf("duplicate fallback question: %q", q)
			}
			seen[q] = true
		}
	})

	t.Run("default follow-ups do not depend on the question", func(t *testing.T) {
		a := defaultFollowUps("what were sales in July?")
		b := defaultFollowUps("")
		if len(a) != len(b) || len(a) == 0 {
			t.Fatalf("expected the same non-empty fallbacks, got %v and %v", a, b)
		}
		for i := range a {
			if a[i] != b[i] {
				t.Errorf("fallback %d differs: %q vs %q", i, a[i], b[i])
			}
		}
	})
}

func TestSummarizeChatHistory(t *testing.T) {
	t.Run("empty history yields an empty summary", func(t *testing.T) {
		if got := summarizeChatHistory(nil); got != "" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("nil turns are skipped and each turn carries its SQL", func(t *testing.T) {
		got := summarizeChatHistory([]*suggestions.ChatTurn{
			nil,
			{Question: "top products?", SQL: "SELECT product FROM demo.sales"},
			{Question: "by region?", SQL: "SELECT region FROM demo.sales"},
		})
		if strings.Count(got, "- Q: ") != 2 {
			t.Errorf("expected 2 turns, got %q", got)
		}
		if !strings.Contains(got, "SELECT product FROM demo.sales") {
			t.Errorf("SQL missing from the summary: %q", got)
		}
		if strings.HasSuffix(got, "\n") {
			t.Error("summary should be trimmed")
		}
	})
}

func TestCopyMetricsToSuggestions(t *testing.T) {
	t.Run("nil in, nil out", func(t *testing.T) {
		if got := copyMetricsToSuggestions(nil); got != nil {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("copies scalars, drops nil entries and does not alias slices", func(t *testing.T) {
		in := &reports.MetricsData{
			PeriodCurrentLabel:  sptr("2026-07"),
			PeriodPreviousLabel: sptr("2026-06"),
			PerfSuggestions:     []string{"add an index on date"},
			Correlations:        []*reports.CorrelationPairData{{ColumnA: "qty", ColumnB: "revenue", Pearson: 0.9}},
			Aggregates: map[string]*reports.AggregateData{
				"revenue": {Sum: f64(100)},
				"dropped": nil,
			},
		}
		got := copyMetricsToSuggestions(in)
		if got.PeriodCurrentLabel == nil || *got.PeriodCurrentLabel != "2026-07" {
			t.Errorf("period label = %v", got.PeriodCurrentLabel)
		}
		if len(got.Aggregates) != 1 || got.Aggregates["revenue"] == nil {
			t.Errorf("nil aggregate not dropped: %+v", got.Aggregates)
		}
		if len(got.Correlations) != 1 || got.Correlations[0].Pearson != 0.9 {
			t.Errorf("correlations = %+v", got.Correlations)
		}
		got.PerfSuggestions[0] = "mutated"
		if in.PerfSuggestions[0] != "add an index on date" {
			t.Error("perf suggestions alias the source payload")
		}
	})
}

func TestAskErrorConstructors(t *testing.T) {
	internal := errTestInternal{}

	if askConnectionForbiddenError(nil) != nil || askConnectionNotFoundError(nil) != nil || askValidationError(nil) != nil {
		t.Error("a nil cause must produce a nil error")
	}

	for _, tc := range []struct {
		name string
		err  error
		code string
	}{
		{"forbidden", askConnectionForbiddenError(internal), "CONNECTION_FORBIDDEN"},
		{"not found", askConnectionNotFoundError(internal), "CONNECTION_NOT_FOUND"},
		{"validation", askValidationError(internal), "VALIDATION_ERROR"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ve, ok := tc.err.(*suggestions.ValidationError)
			if !ok {
				t.Fatalf("expected *suggestions.ValidationError, got %T", tc.err)
			}
			if ve.Code == nil || *ve.Code != tc.code {
				t.Errorf("code = %v, want %q", ve.Code, tc.code)
			}
			if strings.Contains(ve.Message, internalSecretMarker) {
				t.Errorf("internal detail leaked: %q", ve.Message)
			}
		})
	}

	t.Run("llm error keeps the code and hides the cause", func(t *testing.T) {
		// A nil cause still produces an error — the LLM path always reports something.
		nilCause := askLLMError("LLM_UNAVAILABLE", "The model is unavailable.", nil)
		le, ok := nilCause.(*suggestions.LLMError)
		if !ok {
			t.Fatalf("expected *suggestions.LLMError, got %T", nilCause)
		}
		if le.Message != "The model is unavailable." {
			t.Errorf("message = %q", le.Message)
		}

		withCause := askLLMError("LLM_UNAVAILABLE", "The model is unavailable.", internal).(*suggestions.LLMError)
		if withCause.Code == nil || *withCause.Code != "LLM_UNAVAILABLE" {
			t.Errorf("code = %v", withCause.Code)
		}
		if strings.Contains(withCause.Message, internalSecretMarker) {
			t.Errorf("internal detail leaked: %q", withCause.Message)
		}
	})
}
