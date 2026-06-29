package observability

import (
	"strings"
	"testing"
)

func TestPrometheusPoolMetrics_ContainsVersion(t *testing.T) {
	out := PrometheusPoolMetrics("1.2.3", nil)
	if !strings.Contains(out, `pgqn_info{version="1.2.3"} 1`) {
		t.Fatalf("unexpected output: %s", out)
	}
}
