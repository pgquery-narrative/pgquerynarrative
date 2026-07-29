package service

import "testing"

func TestPctChange(t *testing.T) {
	if got := pctChange(100, 340); got < 239.9 || got > 240.1 {
		t.Fatalf("expected 240%%, got %v", got)
	}
	if pctChange(0, 100) != 0 {
		t.Fatal("zero baseline should return 0")
	}
}

func TestClassifyImpact(t *testing.T) {
	cfg := RegressionPollerConfig{CriticalThresholdPct: 200, HighThresholdPct: 100}
	if classifyImpact(250, cfg) != "critical" {
		t.Fatal("expected critical")
	}
	if classifyImpact(150, cfg) != "high" {
		t.Fatal("expected high")
	}
	if classifyImpact(60, cfg) != "medium" {
		t.Fatal("expected medium")
	}
}

func TestRegressionTitle(t *testing.T) {
	long := regressionTitle("SELECT " + string(make([]byte, 100)))
	if len(long) > 50 {
		t.Fatalf("title too long: %q", long)
	}
}
