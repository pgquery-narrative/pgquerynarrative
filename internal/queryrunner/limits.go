package queryrunner

// DefaultMaxRows is the row cap when configuration is missing or invalid.
const DefaultMaxRows = 1000

// AbsoluteMaxRows is the hard ceiling for result slice capacity and runner config.
// It bounds memory allocation even if callers pass an oversized limit or maxRows.
const AbsoluteMaxRows = 10000

// DefaultMaxResultBytes is the default approximate maximum response payload size.
const DefaultMaxResultBytes = 10 * 1024 * 1024

// DefaultMaxCellBytes is the default approximate maximum size for a single cell.
const DefaultMaxCellBytes = 1 * 1024 * 1024

// DefaultMaxColumns is the default maximum number of columns in a result set.
const DefaultMaxColumns = 100

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

func capPositive(n, fallback int) int {
	if n <= 0 {
		return fallback
	}
	return n
}
