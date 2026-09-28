package audit

import (
	"context"
	"testing"
)

func TestParseMode(t *testing.T) {
	cases := map[string]Mode{
		"":            ModeBestEffort,
		"best_effort": ModeBestEffort,
		"BEST_EFFORT": ModeBestEffort,
		"required":    ModeRequired,
		"buffered":    ModeBuffered,
		"bogus":       ModeBestEffort,
	}
	for in, want := range cases {
		if got := ParseMode(in); got != want {
			t.Errorf("ParseMode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidMode(t *testing.T) {
	for _, v := range []string{"", "best_effort", "required", "buffered"} {
		if !ValidMode(v) {
			t.Errorf("ValidMode(%q) = false, want true", v)
		}
	}
	if ValidMode("bogus") {
		t.Error(`ValidMode("bogus") = true, want false`)
	}
}

func TestStore_NilAndNoPool(t *testing.T) {
	var nilStore *Store
	if err := nilStore.Record(context.Background(), Entry{EventType: EventAPIRequest}); err != nil {
		t.Errorf("nil store Record() = %v, want nil", err)
	}
	nilStore.Close() // must not panic

	s := NewStore(nil, ModeRequired)
	if err := s.Record(context.Background(), Entry{EventType: EventRunQuery, HighRisk: true}); err != nil {
		t.Errorf("store with nil pool Record() = %v, want nil (no-op)", err)
	}
	if s.Mode() != ModeRequired {
		t.Errorf("Mode() = %q, want required", s.Mode())
	}
	replayed, remaining, err := s.ReplayBuffered(context.Background(), 10)
	if err != nil || replayed != 0 || remaining != 0 {
		t.Errorf("ReplayBuffered on nil pool = (%d, %d, %v), want (0, 0, nil)", replayed, remaining, err)
	}
}

func TestNewStore_BufferedModeRequiresPool(t *testing.T) {
	// ModeBuffered with a nil pool must not start a background worker (no pool to write to).
	s := NewStore(nil, ModeBuffered)
	if s.queue != nil {
		t.Error("expected no queue to be created when pool is nil, even in ModeBuffered")
	}
	s.Close() // must be a safe no-op
}

func TestMode_DefaultsToBestEffortForUnknownValues(t *testing.T) {
	s := &Store{mode: ParseMode("totally-bogus")}
	if s.Mode() != ModeBestEffort {
		t.Errorf("Mode() = %q, want best_effort default", s.Mode())
	}
}
