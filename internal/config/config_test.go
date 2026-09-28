package config

import (
	"os"
	"testing"
)

func TestValidateMetricsConfig_ClampsToRange(t *testing.T) {
	tests := []struct {
		name      string
		in        MetricsConfig
		wantSigma float64
		wantTrend int
		wantMA    int
	}{
		{"defaults unchanged", MetricsConfig{TrendThresholdPercent: 0.5, AnomalySigma: 2.0, TrendPeriods: 6, MovingAvgWindow: 3}, 2.0, 6, 3},
		{"sigma low clamped", MetricsConfig{TrendThresholdPercent: 0.5, AnomalySigma: 0.5, TrendPeriods: 6, MovingAvgWindow: 3}, 1.0, 6, 3},
		{"sigma high clamped", MetricsConfig{TrendThresholdPercent: 0.5, AnomalySigma: 10, TrendPeriods: 6, MovingAvgWindow: 3}, 5.0, 6, 3},
		{"trend low clamped", MetricsConfig{TrendThresholdPercent: 0.5, AnomalySigma: 2, TrendPeriods: 1, MovingAvgWindow: 3}, 2.0, 2, 3},
		{"trend high clamped", MetricsConfig{TrendThresholdPercent: 0.5, AnomalySigma: 2, TrendPeriods: 100, MovingAvgWindow: 3}, 2.0, 24, 3},
		{"ma low clamped", MetricsConfig{TrendThresholdPercent: 0.5, AnomalySigma: 2, TrendPeriods: 6, MovingAvgWindow: 1}, 2.0, 6, 2},
		{"ma high clamped", MetricsConfig{TrendThresholdPercent: 0.5, AnomalySigma: 2, TrendPeriods: 6, MovingAvgWindow: 50}, 2.0, 6, 24},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := validateMetricsConfig(tt.in)
			if got.AnomalySigma != tt.wantSigma {
				t.Errorf("AnomalySigma = %v, want %v", got.AnomalySigma, tt.wantSigma)
			}
			if got.TrendPeriods != tt.wantTrend {
				t.Errorf("TrendPeriods = %v, want %v", got.TrendPeriods, tt.wantTrend)
			}
			if got.MovingAvgWindow != tt.wantMA {
				t.Errorf("MovingAvgWindow = %v, want %v", got.MovingAvgWindow, tt.wantMA)
			}
		})
	}
}

func TestValidateMetricsConfig_MaxTimeSeriesPeriods(t *testing.T) {
	tests := []struct {
		in   int
		want int
	}{
		{0, 2},
		{1, 2},
		{24, 24},
		{120, 120},
		{200, 120},
	}
	for _, tt := range tests {
		got := validateMetricsConfig(MetricsConfig{MaxTimeSeriesPeriods: tt.in})
		if got.MaxTimeSeriesPeriods != tt.want {
			t.Errorf("MaxTimeSeriesPeriods(%d) = %d, want %d", tt.in, got.MaxTimeSeriesPeriods, tt.want)
		}
	}
}

func TestLoad_Logging_LogPrettyDefault(t *testing.T) {
	save := os.Getenv("LOG_PRETTY")
	defer func() { _ = os.Setenv("LOG_PRETTY", save) }()

	// Unset → default pretty true (readable local output)
	_ = os.Unsetenv("LOG_PRETTY")
	cfg := Load()
	if !cfg.Logging.Pretty {
		t.Errorf("when LOG_PRETTY unset, Logging.Pretty should be true, got false")
	}

	// Explicit false → JSON-style output
	_ = os.Setenv("LOG_PRETTY", "false")
	cfg = Load()
	if cfg.Logging.Pretty {
		t.Errorf("when LOG_PRETTY=false, Logging.Pretty should be false, got true")
	}

	_ = os.Setenv("LOG_PRETTY", "1")
	cfg = Load()
	if !cfg.Logging.Pretty {
		t.Errorf("when LOG_PRETTY=1, Logging.Pretty should be true, got false")
	}
}
