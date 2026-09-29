package service

import "math"

// clampInt32 converts n to int32, saturating at the int32 bounds instead of
// wrapping/overflowing. Used when projecting internal counts (row counts,
// aggregate counts, period counts) onto API types that use int32 for
// transport compatibility; those internal counts are already bounded by
// query row limits, but this makes the conversion safe even if a caller's
// limit configuration changes.
func clampInt32(n int) int32 {
	if n > math.MaxInt32 {
		return math.MaxInt32
	}
	if n < math.MinInt32 {
		return math.MinInt32
	}
	return int32(n)
}
