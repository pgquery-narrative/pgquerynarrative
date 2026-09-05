package service

import (
	"strings"
	"testing"
	"time"

	schedules "github.com/pgquerynarrative/pgquerynarrative/api/gen/schedules"
)

func TestComputeNextRun(t *testing.T) {
	base := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	t.Run("valid @every advances by the duration", func(t *testing.T) {
		got, err := computeNextRun("@every 15m", base)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if want := base.Add(15 * time.Minute); !got.Equal(want) {
			t.Errorf("next run = %v, want %v", got, want)
		}
	})

	t.Run("surrounding whitespace is tolerated", func(t *testing.T) {
		got, err := computeNextRun("  @every  2h  ", base)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if want := base.Add(2 * time.Hour); !got.Equal(want) {
			t.Errorf("next run = %v, want %v", got, want)
		}
	})

	for _, tc := range []struct{ name, expr string }{
		{"cron syntax is not supported", "*/5 * * * *"},
		{"missing prefix", "15m"},
		{"unparseable duration", "@every banana"},
		{"zero duration would busy-loop", "@every 0s"},
		{"negative duration would run in the past", "@every -5m"},
		{"empty", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := computeNextRun(tc.expr, base); err == nil {
				t.Errorf("expected error for %q", tc.expr)
			}
		})
	}
}

func TestValidateScheduleInput(t *testing.T) {
	// A public IP literal short-circuits ValidateWebhookURL's DNS lookup, so this
	// test exercises the validator's own rules without depending on name resolution.
	target := "https://93.184.216.34/pgqn"
	allowed := []string{"93.184.216.34"}

	valid := func() *schedules.ScheduleInput {
		return &schedules.ScheduleInput{
			Name:              "nightly revenue",
			IntervalExpr:      "@every 24h",
			DestinationType:   "webhook",
			DestinationTarget: &target,
		}
	}

	t.Run("accepts a well-formed webhook schedule", func(t *testing.T) {
		if err := validateScheduleInput(valid(), allowed); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("log destination needs no target", func(t *testing.T) {
		in := valid()
		in.DestinationType = "LOG" // case-insensitive
		in.DestinationTarget = nil
		if err := validateScheduleInput(in, allowed); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("webhook host outside the allowlist is rejected", func(t *testing.T) {
		in := valid()
		other := "https://198.51.100.7/pgqn"
		in.DestinationTarget = &other
		if err := validateScheduleInput(in, allowed); err == nil {
			t.Error("expected a host-allowlist rejection")
		}
	})

	t.Run("webhook without a target is rejected", func(t *testing.T) {
		in := valid()
		in.DestinationTarget = nil
		if err := validateScheduleInput(in, allowed); err == nil {
			t.Error("expected destination_target to be required")
		}
	})

	for _, tc := range []struct {
		name   string
		mutate func(*schedules.ScheduleInput)
	}{
		{"blank name", func(in *schedules.ScheduleInput) { in.Name = "   " }},
		{"blank interval", func(in *schedules.ScheduleInput) { in.IntervalExpr = "" }},
		{"bad interval", func(in *schedules.ScheduleInput) { in.IntervalExpr = "@every nope" }},
		{"unknown destination type", func(in *schedules.ScheduleInput) { in.DestinationType = "email" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := valid()
			tc.mutate(in)
			if err := validateScheduleInput(in, allowed); err == nil {
				t.Errorf("expected rejection for %s", tc.name)
			}
		})
	}
}

func TestScheduleCoalesceHelpers(t *testing.T) {
	t.Run("firstNonBlank", func(t *testing.T) {
		if got := firstNonBlank("  ", "fallback"); got != "fallback" {
			t.Errorf("blank → fallback, got %q", got)
		}
		if got := firstNonBlank("  value  ", "fallback"); got != "value" {
			t.Errorf("expected trimmed value, got %q", got)
		}
	})

	t.Run("coalescePtrOrBlank turns an explicit blank into a NULL", func(t *testing.T) {
		blank := "   "
		if got := coalescePtrOrBlank(&blank, "fallback"); got != nil {
			t.Errorf("explicit blank must clear the column, got %q", *got)
		}
		set := "kept"
		if got := coalescePtrOrBlank(&set, "fallback"); got == nil || *got != "kept" {
			t.Errorf("explicit value must win over the fallback, got %v", got)
		}
		if got := coalescePtrOrBlank(nil, "fallback"); got == nil || *got != "fallback" {
			t.Errorf("nil → fallback, got %v", got)
		}
		if got := coalescePtrOrBlank(nil, "  "); got != nil {
			t.Errorf("nil with blank fallback → NULL, got %q", *got)
		}
	})

	t.Run("coalesceStrPtr prefers the explicit pointer", func(t *testing.T) {
		v, fb := "v", "fb"
		if got := coalesceStrPtr(&v, &fb); got != &v {
			t.Error("explicit pointer should be returned as-is")
		}
		if got := coalesceStrPtr(nil, &fb); got != &fb {
			t.Error("nil → fallback pointer")
		}
		if got := coalesceStrPtr(nil, nil); got != nil {
			t.Error("nil/nil → nil")
		}
	})

	t.Run("coalesceBoolPtr keeps an explicit false", func(t *testing.T) {
		f := false
		if got := coalesceBoolPtr(&f, true); got == nil || *got != false {
			t.Error("an explicit false must not be replaced by the fallback")
		}
		if got := coalesceBoolPtr(nil, true); got == nil || *got != true {
			t.Error("nil → fallback")
		}
	})

	t.Run("nullInt maps the zero value to SQL NULL", func(t *testing.T) {
		if got := nullInt(0); got != nil {
			t.Errorf("0 → NULL, got %v", got)
		}
		if got := nullInt(7); got != 7 {
			t.Errorf("7 → 7, got %v", got)
		}
	})

	t.Run("ptrString", func(t *testing.T) {
		if got := ptrString(nil); got != "" {
			t.Errorf("nil → \"\", got %q", got)
		}
		s := "x"
		if got := ptrString(&s); got != "x" {
			t.Errorf("got %q", got)
		}
	})
}

func TestScheduleErrorConstructorsDoNotLeakInternals(t *testing.T) {
	internal := errTestInternal{}

	t.Run("nil in, nil out", func(t *testing.T) {
		if scheduleValidationError(nil) != nil ||
			scheduleConnectionNotFoundError(nil) != nil ||
			scheduleConnectionForbiddenError(nil) != nil {
			t.Error("a nil cause must produce a nil error")
		}
	})

	for _, tc := range []struct {
		name string
		fn   func(error) error
		code string
	}{
		{"validation", scheduleValidationError, "VALIDATION_ERROR"},
		{"not found", scheduleConnectionNotFoundError, "CONNECTION_NOT_FOUND"},
		{"forbidden", scheduleConnectionForbiddenError, "CONNECTION_FORBIDDEN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.fn(internal)
			ve, ok := err.(*schedules.ValidationError)
			if !ok {
				t.Fatalf("expected *schedules.ValidationError, got %T", err)
			}
			if ve.Code == nil || *ve.Code != tc.code {
				t.Errorf("code = %v, want %q", ve.Code, tc.code)
			}
			if strings.Contains(ve.Message, internalSecretMarker) {
				t.Errorf("internal detail leaked to the API surface: %q", ve.Message)
			}
		})
	}
}

func TestWebhookAllowedHostsReturnsACopy(t *testing.T) {
	var nilSvc *SchedulesService
	if got := nilSvc.WebhookAllowedHosts(); got != nil {
		t.Errorf("nil receiver → nil, got %v", got)
	}

	svc := &SchedulesService{allowedHosts: []string{"a.example.com", "b.example.com"}}
	got := svc.webhookAllowedHosts()
	if len(got) != 2 {
		t.Fatalf("expected 2 hosts, got %v", got)
	}
	got[0] = "mutated.example.com"
	if svc.allowedHosts[0] != "a.example.com" {
		t.Error("callers must not be able to mutate the service's allowlist")
	}
}

const internalSecretMarker = "postgres://user:hunter2@db.internal:5432"

type errTestInternal struct{}

func (errTestInternal) Error() string {
	return "dial " + internalSecretMarker + ": connection refused"
}
