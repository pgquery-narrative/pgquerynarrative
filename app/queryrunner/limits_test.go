package queryrunner

import "testing"

func TestCapRowCount(t *testing.T) {
	tests := []struct {
		name string
		in   int
		want int
	}{
		{"zero uses default", 0, DefaultMaxRows},
		{"negative uses default", -1, DefaultMaxRows},
		{"within range", 500, 500},
		{"at ceiling", AbsoluteMaxRows, AbsoluteMaxRows},
		{"above ceiling", AbsoluteMaxRows + 1, AbsoluteMaxRows},
		{"max int", 1<<31 - 1, AbsoluteMaxRows},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := capRowCount(tt.in); got != tt.want {
				t.Fatalf("capRowCount(%d) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}
