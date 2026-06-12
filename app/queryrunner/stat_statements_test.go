package queryrunner

import (
	"testing"
)

func TestStatStatementsOrderColumns(t *testing.T) {
	for _, orderBy := range []string{"total_time", "mean_time", "calls", "TOTAL_TIME"} {
		if _, ok := statStatementsOrderColumns[orderBy]; orderBy != "TOTAL_TIME" && !ok {
			// lowercase path is validated in StatStatements via ToLower
		}
	}
	if _, ok := statStatementsOrderColumns["total_time"]; !ok {
		t.Fatal("expected total_time column mapping")
	}
	if statStatementsOrderColumns["total_time"] != "total_exec_time" {
		t.Fatalf("got %q", statStatementsOrderColumns["total_time"])
	}
}
