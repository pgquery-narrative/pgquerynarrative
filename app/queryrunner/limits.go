package queryrunner

// DefaultMaxRows is the row cap when configuration is missing or invalid.
const DefaultMaxRows = 1000

// AbsoluteMaxRows is the hard ceiling for result slice capacity and runner config.
// It bounds memory allocation even if callers pass an oversized limit or maxRows.
const AbsoluteMaxRows = 10000

// capRowCount returns a safe slice capacity in [1, AbsoluteMaxRows].
func capRowCount(n int) int {
	if n <= 0 {
		return DefaultMaxRows
	}
	if n > AbsoluteMaxRows {
		return AbsoluteMaxRows
	}
	return n
}
