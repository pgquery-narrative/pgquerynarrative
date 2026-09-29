package queryrunner

import (
	"testing"
)

func TestStatStatementsOrderColumns(t *testing.T) {
	for _, orderBy := range []string{"total_time", "mean_time", "calls"} {
		if _, ok := statStatementsOrderColumns[orderBy]; !ok {
			t.Fatalf("expected column mapping for %q", orderBy)
		}
	}
	if _, ok := statStatementsOrderColumns["total_time"]; !ok {
		t.Fatal("expected total_time column mapping")
	}
	if statStatementsOrderColumns["total_time"] != "total_exec_time" {
		t.Fatalf("got %q", statStatementsOrderColumns["total_time"])
	}
}
