package service

import (
	"context"
	"regexp"
	"strconv"
	"testing"
	"time"
)

// The guided scenarios must never ship a hardcoded calendar date: both seeds
// generate rows rolling backwards from CURRENT_DATE, so a frozen literal goes
// empty as soon as the window moves past it and the first click of the demo
// returns nothing.
func TestDemoScenarios_NoHardcodedCalendarDates(t *testing.T) {
	s := &WorkspaceService{} // no queriesSvc: exercises the fallback path
	list, err := s.DemoScenarios(context.Background())
	if err != nil {
		t.Fatalf("DemoScenarios: %v", err)
	}
	if len(list.Items) == 0 {
		t.Fatal("expected scenarios")
	}

	wantMonth := monthStart(time.Now().UTC().AddDate(0, -1, 0))
	dateRe := regexp.MustCompile(`\d{4}-\d{2}-\d{2}`)

	for _, item := range list.Items {
		for _, lit := range dateRe.FindAllString(item.SQL, -1) {
			got, err := time.Parse("2006-01-02", lit)
			if err != nil {
				t.Fatalf("scenario %q: unparseable date literal %q", item.ID, lit)
			}
			if !monthStart(got).Equal(wantMonth) {
				t.Fatalf("scenario %q: date literal %q is not in the live seed window (want month %s) — a frozen literal returns zero rows",
					item.ID, lit, wantMonth.Format("2006-01"))
			}
		}
		// EXTRACT(YEAR ...) / EXTRACT(MONTH ...) must track the same month.
		if y := regexp.MustCompile(`EXTRACT\(YEAR FROM date\) = (\d+)`).FindStringSubmatch(item.SQL); y != nil {
			if n, _ := strconv.Atoi(y[1]); n != wantMonth.Year() {
				t.Fatalf("scenario %q: EXTRACT(YEAR)=%d, want %d", item.ID, n, wantMonth.Year())
			}
		}
		if m := regexp.MustCompile(`EXTRACT\(MONTH FROM date\) = (\d+)`).FindStringSubmatch(item.SQL); m != nil {
			if n, _ := strconv.Atoi(m[1]); n != int(wantMonth.Month()) {
				t.Fatalf("scenario %q: EXTRACT(MONTH)=%d, want %d", item.ID, n, int(wantMonth.Month()))
			}
		}
	}
}

// The DATE_TRUNC scenario is the entry point of the guided demo, and the
// rewrite engine only unwraps a constant that is aligned to the truncation
// unit (see PR1 / tryRewriteDateTruncEquality). A month-start literal keeps
// that scenario rewritable; any other day silently yields zero candidates.
func TestDemoScenarios_DateTruncLiteralIsMonthAligned(t *testing.T) {
	s := &WorkspaceService{}
	list, err := s.DemoScenarios(context.Background())
	if err != nil {
		t.Fatalf("DemoScenarios: %v", err)
	}
	var found bool
	for _, item := range list.Items {
		if item.ID != "slow-dashboard" {
			continue
		}
		found = true
		m := regexp.MustCompile(`DATE_TRUNC\('month', date\) = DATE '(\d{4}-\d{2}-\d{2})'`).FindStringSubmatch(item.SQL)
		if m == nil {
			t.Fatalf("slow-dashboard no longer carries a DATE_TRUNC month equality: %s", item.SQL)
		}
		d, err := time.Parse("2006-01-02", m[1])
		if err != nil {
			t.Fatalf("unparseable literal %q", m[1])
		}
		if d.Day() != 1 {
			t.Fatalf("DATE_TRUNC literal %q is not month-aligned; the rewriter would (correctly) refuse to rewrite it, so the guided demo would propose nothing", m[1])
		}
	}
	if !found {
		t.Fatal("slow-dashboard scenario missing")
	}
}

func TestMonthStart(t *testing.T) {
	got := monthStart(time.Date(2026, 3, 17, 13, 45, 0, 0, time.UTC))
	want := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("monthStart = %s, want %s", got, want)
	}
}
