package service

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	queries "github.com/pgquerynarrative/pgquerynarrative/api/gen/queries"
	"github.com/pgquerynarrative/pgquerynarrative/app/charts"
	apperrors "github.com/pgquerynarrative/pgquerynarrative/app/errors"
	"github.com/pgquerynarrative/pgquerynarrative/app/queryrunner"
)

func TestTruncateRunes(t *testing.T) {
	t.Run("short strings pass through trimmed", func(t *testing.T) {
		if got := truncateRunes("  hello  ", 20); got != "hello" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("a non-positive max disables truncation", func(t *testing.T) {
		if got := truncateRunes("hello", 0); got != "hello" {
			t.Errorf("got %q", got)
		}
		if got := truncateRunes("hello", -1); got != "hello" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("truncation appends an ellipsis", func(t *testing.T) {
		got := truncateRunes("abcdefghij", 4)
		if got != "abcd…" {
			t.Errorf("got %q, want \"abcd…\"", got)
		}
	})

	t.Run("multi-byte runes are counted, not bytes", func(t *testing.T) {
		// Five runes, fifteen bytes: a byte-based cut would split a character.
		got := truncateRunes("日本語です。", 3)
		if got != "日本語…" {
			t.Errorf("got %q, want \"日本語…\"", got)
		}
	})

	t.Run("an exact-length string is not truncated", func(t *testing.T) {
		if got := truncateRunes("abcd", 4); got != "abcd" {
			t.Errorf("got %q", got)
		}
	})
}

func TestTruncateQuery(t *testing.T) {
	t.Run("short queries pass through trimmed", func(t *testing.T) {
		if got := truncateQuery("  SELECT 1  ", 100); got != "SELECT 1" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("long queries are cut to max with an ellipsis", func(t *testing.T) {
		long := strings.Repeat("x", 50)
		got := truncateQuery(long, 20)
		if len(got) != 20 {
			t.Errorf("expected exactly 20 characters, got %d (%q)", len(got), got)
		}
		if !strings.HasSuffix(got, "...") {
			t.Errorf("expected a trailing ellipsis, got %q", got)
		}
	})

	t.Run("the production cap is respected", func(t *testing.T) {
		long := strings.Repeat("SELECT 1; ", 2000)
		got := truncateQuery(long, queryrunner.StatStatementsQueryMaxLen)
		if len(got) > queryrunner.StatStatementsQueryMaxLen {
			t.Errorf("query exceeds the column cap: %d > %d", len(got), queryrunner.StatStatementsQueryMaxLen)
		}
	})
}

func TestErrString(t *testing.T) {
	if got := errString(nil); got != "nil" {
		t.Errorf("nil → %q, want \"nil\"", got)
	}
	if got := errString(errors.New("boom")); got != "boom" {
		t.Errorf("got %q", got)
	}
}

func TestBaselineWindowDays(t *testing.T) {
	t.Run("an explicit window wins", func(t *testing.T) {
		c := RegressionPollerConfig{BaselineWindowDays: 7, RetentionDays: 30}
		if got := c.baselineWindowDays(); got != 7 {
			t.Errorf("got %d, want 7", got)
		}
	})

	t.Run("retention is the next fallback", func(t *testing.T) {
		c := RegressionPollerConfig{RetentionDays: 30}
		if got := c.baselineWindowDays(); got != 30 {
			t.Errorf("got %d, want 30", got)
		}
	})

	t.Run("an unconfigured poller uses the 14-day default", func(t *testing.T) {
		if got := (RegressionPollerConfig{}).baselineWindowDays(); got != 14 {
			t.Errorf("got %d, want 14", got)
		}
	})

	t.Run("non-positive values fall through rather than producing an empty window", func(t *testing.T) {
		c := RegressionPollerConfig{BaselineWindowDays: -3, RetentionDays: 0}
		if got := c.baselineWindowDays(); got != 14 {
			t.Errorf("got %d, want the 14-day default", got)
		}
	})
}

func TestScheduleWorkerIDIsStableAndNonEmpty(t *testing.T) {
	a := scheduleWorkerID()
	if strings.TrimSpace(a) == "" {
		t.Fatal("a worker id is required to claim schedule leases")
	}
	if b := scheduleWorkerID(); b != a {
		t.Errorf("worker id must be stable within a process: %q vs %q", a, b)
	}
}

func TestSanitizeValidationMessage(t *testing.T) {
	t.Run("known sentinels are mapped to their own text", func(t *testing.T) {
		for _, sentinel := range []error{
			apperrors.ErrQueryTooLong,
			apperrors.ErrOnlySelectAllowed,
			apperrors.ErrDisallowedKeyword,
			apperrors.ErrSchemaNotAllowed,
			apperrors.ErrUnqualifiedTable,
			apperrors.ErrMultipleStatements,
		} {
			msg := "query validation failed: " + sentinel.Error()
			if got := sanitizeValidationMessage(msg); got != sentinel.Error() {
				t.Errorf("got %q, want %q", got, sentinel.Error())
			}
		}
	})

	t.Run("an unrecognised cause collapses to a generic message", func(t *testing.T) {
		msg := "query validation failed: pq: relation \"secret_internal\" does not exist"
		got := sanitizeValidationMessage(msg)
		if got != "Query validation failed." {
			t.Errorf("got %q", got)
		}
		if strings.Contains(got, "secret_internal") {
			t.Error("internal schema detail leaked to the caller")
		}
	})

	t.Run("a message without the prefix is generic too", func(t *testing.T) {
		if got := sanitizeValidationMessage("some other failure"); got != "Query validation failed." {
			t.Errorf("got %q", got)
		}
	})
}

func TestBoolPtrIf(t *testing.T) {
	if got := boolPtrIf(false); got != nil {
		t.Error("false must be omitted from the JSON, not sent as false")
	}
	if got := boolPtrIf(true); got == nil || *got != true {
		t.Errorf("got %v", got)
	}
}

func TestSuggestToQueries(t *testing.T) {
	if got := suggestToQueries(nil); got != nil {
		t.Errorf("no suggestions → nil, got %v", got)
	}
	got := suggestToQueries([]charts.Suggestion{
		{ChartType: "line", Label: "Revenue by month", Reason: "time series"},
		{ChartType: "bar", Label: "Revenue by region", Reason: "categorical"},
	})
	if len(got) != 2 {
		t.Fatalf("expected 2 suggestions, got %d", len(got))
	}
	if got[0].ChartType != "line" || got[1].Label != "Revenue by region" {
		t.Errorf("fields not carried across: %+v %+v", got[0], got[1])
	}
}

func TestPeriodComparisonToAPI(t *testing.T) {
	prev, change, pct := 900.0, 300.0, 33.3
	out := &queryrunner.PeriodComparisonOutput{Comparisons: []queryrunner.PeriodComparison{
		{Measure: "revenue", Current: 1200, Trend: "up", Previous: &prev, Change: &change, ChangePercentage: &pct},
		{Measure: "orders", Current: 50, Trend: "flat"},
	}}
	items := periodComparisonToAPI(out)
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0].Previous == nil || *items[0].Previous != prev ||
		items[0].Change == nil || items[0].ChangePercentage == nil {
		t.Errorf("optional fields dropped: %+v", items[0])
	}
	// A measure with no prior period must stay nil rather than reporting a 0% change.
	if items[1].Previous != nil || items[1].Change != nil || items[1].ChangePercentage != nil {
		t.Errorf("absent comparison should stay nil, got %+v", items[1])
	}

	t.Run("no comparisons yields an empty, non-nil slice", func(t *testing.T) {
		got := periodComparisonToAPI(&queryrunner.PeriodComparisonOutput{})
		if got == nil || len(got) != 0 {
			t.Errorf("got %v", got)
		}
	})
}

func TestTimeSeriesToPeriodComparisonFallback(t *testing.T) {
	t.Run("fewer than two rows cannot form a comparison", func(t *testing.T) {
		items, cur, prev := timeSeriesToPeriodComparisonFallback(
			[]string{"month", "revenue"}, [][]interface{}{{"2026-07-01", 1200.0}}, nil)
		if items != nil || cur != "" || prev != "" {
			t.Errorf("got %v / %q / %q", items, cur, prev)
		}
	})

	t.Run("a two-period series produces a labelled comparison", func(t *testing.T) {
		items, cur, prev := timeSeriesToPeriodComparisonFallback(
			// The profiler only marks a column as a time series when its values parse
			// as dates, so full ISO dates are what actually exercises this path.
			[]string{"month", "revenue"},
			[][]interface{}{{"2026-06-01", 900.0}, {"2026-07-01", 1200.0}},
			nil,
		)
		if len(items) == 0 {
			t.Fatal("expected at least one measure")
		}
		if cur == "" || prev == "" {
			t.Errorf("expected period labels, got %q / %q", cur, prev)
		}
		var revenue *queries.PeriodComparisonItem
		for _, it := range items {
			if it.Measure == "revenue" {
				revenue = it
			}
		}
		if revenue == nil {
			t.Fatalf("revenue measure missing from %+v", items)
		}
		if revenue.Current != 1200 {
			t.Errorf("current = %v, want 1200", revenue.Current)
		}
		if revenue.Previous == nil || *revenue.Previous != 900 {
			t.Errorf("previous = %v, want 900", revenue.Previous)
		}
	})
}

func TestQueriesConnectionErrorConstructors(t *testing.T) {
	internal := errTestInternal{}
	if connectionNotFoundQueriesError(nil) != nil || connectionForbiddenQueriesError(nil) != nil {
		t.Error("a nil cause must produce a nil error")
	}
	for _, tc := range []struct {
		err  error
		code string
		msg  string
	}{
		{connectionNotFoundQueriesError(internal), "CONNECTION_NOT_FOUND", "connection not found"},
		{connectionForbiddenQueriesError(internal), "CONNECTION_FORBIDDEN", "connection access denied"},
	} {
		ve, ok := tc.err.(*queries.ValidationError)
		if !ok {
			t.Fatalf("expected *queries.ValidationError, got %T", tc.err)
		}
		if ve.Code == nil || *ve.Code != tc.code {
			t.Errorf("code = %v, want %q", ve.Code, tc.code)
		}
		if ve.Message != tc.msg {
			t.Errorf("message = %q, want %q", ve.Message, tc.msg)
		}
		if strings.Contains(ve.Message, internalSecretMarker) {
			t.Errorf("internal detail leaked: %q", ve.Message)
		}
	}
}

func TestConvertStoredMetrics(t *testing.T) {
	t.Run("invalid JSON yields nil rather than a panic", func(t *testing.T) {
		if got := ConvertStoredMetrics([]byte("not json")); got != nil {
			t.Errorf("got %+v", got)
		}
	})

	t.Run("an empty object still yields a usable metrics struct", func(t *testing.T) {
		if got := ConvertStoredMetrics([]byte(`{}`)); got == nil {
			t.Error("expected a non-nil metrics struct")
		}
	})

	t.Run("the structured investigation extra survives the round trip", func(t *testing.T) {
		raw, err := json.Marshal(map[string]any{
			"investigation": map[string]any{"id": "inv-1", "partitions_before": 50},
		})
		if err != nil {
			t.Fatal(err)
		}
		got := ConvertStoredMetrics(raw)
		if got == nil || got.Investigation == nil {
			t.Fatalf("investigation extra was dropped: %+v", got)
		}
		m, ok := got.Investigation.(map[string]any)
		if !ok || m["id"] != "inv-1" {
			t.Errorf("investigation payload = %+v", got.Investigation)
		}
	})
}

func TestFormatFloatForPrompt(t *testing.T) {
	for _, tc := range []struct {
		in   float64
		want string
	}{
		{1234.5678, "1235"},
		{0.000123456, "0.0001235"},
		{0, "0"},
		{-42.5, "-42.5"},
	} {
		if got := formatFloatForPrompt(tc.in); got != tc.want {
			t.Errorf("formatFloatForPrompt(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestWebhookBackoffIsBoundedAndMonotonic(t *testing.T) {
	prev := webhookBackoff(0)
	if webhookBackoff(-1) != prev {
		t.Error("a negative attempt count should be clamped to 0")
	}
	for attempt := 1; attempt < 12; attempt++ {
		d := webhookBackoff(attempt)
		if d < prev {
			t.Errorf("backoff decreased at attempt %d: %v < %v", attempt, d, prev)
		}
		if d > webhookRetryMaxBackoff {
			t.Errorf("backoff exceeded the cap at attempt %d: %v > %v", attempt, d, webhookRetryMaxBackoff)
		}
		prev = d
	}
	if webhookBackoff(64) != webhookRetryMaxBackoff {
		t.Error("a very large attempt count must saturate at the cap, not overflow")
	}
}
