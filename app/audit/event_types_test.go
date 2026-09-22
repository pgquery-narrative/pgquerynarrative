package audit

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"
)

// The event_type CHECK constraint is defined by the newest migration that adds it. Every event type
// the code emits must be allowed there: a missing one is rejected by PostgreSQL, so the event is
// lost (best_effort) or the request fails after its change was applied (required).
func TestEveryEventTypeIsAllowedByTheLatestConstraint(t *testing.T) {
	files, err := filepath.Glob("../db/migrations/*.up.sql")
	if err != nil || len(files) == 0 {
		t.Skip("migrations not found")
	}
	sort.Strings(files)
	add := regexp.MustCompile(`(?s)ADD CONSTRAINT audit_logs_event_type_check CHECK \(event_type IN \((.*?)\)\s*\)`)
	var latest string
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if m := add.FindSubmatch(b); m != nil {
			latest = string(m[1])
		}
	}
	if latest == "" {
		t.Fatal("no migration defines audit_logs_event_type_check")
	}
	allowed := map[string]bool{}
	for _, m := range regexp.MustCompile(`'([A-Z_]+)'`).FindAllStringSubmatch(latest, -1) {
		allowed[m[1]] = true
	}
	for _, e := range AllEventTypes() {
		if !allowed[e] {
			t.Errorf("event type %s is emitted by the code but rejected by the latest audit_logs_event_type_check", e)
		}
	}
}
