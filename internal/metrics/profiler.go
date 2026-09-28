package metrics

import (
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// measureNameSubstrs and dimensionNameSubstrs avoid per-call slice allocations in contains()
var (
	measureNameSubstrs   = []string{"total", "sum", "amount", "revenue", "sales", "count", "avg", "average", "price", "cost"}
	dimensionNameSubstrs = []string{"id", "num", "number", "code"}
)

// ProfileColumns analyzes query result columns to determine their types and roles
func ProfileColumns(columns []string, rows [][]interface{}) []ColumnProfile {
	if len(columns) == 0 {
		return nil
	}

	profiles := make([]ColumnProfile, len(columns))

	// Analyze each column
	for i, colName := range columns {
		profile := ColumnProfile{
			Name: colName,
		}

		// Sample first 100 rows to determine type
		sampleSize := len(rows)
		if sampleSize > 100 {
			sampleSize = 100
		}

		if sampleSize > 0 {
			profile = analyzeColumn(colName, rows[:sampleSize], i)
		} else {
			// Empty result set - default to text
			profile.Type = ColumnTypeText
		}

		profiles[i] = profile
	}

	return profiles
}

// analyzeColumn determines the type and characteristics of a column
func analyzeColumn(name string, rows [][]interface{}, colIndex int) ColumnProfile {
	profile := ColumnProfile{
		Name: name,
	}

	numericCount := 0
	dateCount := 0
	booleanCount := 0
	nullCount := 0

	for _, row := range rows {
		if colIndex >= len(row) {
			continue
		}

		val := row[colIndex]
		if val == nil {
			nullCount++
			continue
		}

		switch v := val.(type) {
		case float64, int64, int32, int:
			numericCount++
		case pgtype.Numeric:
			if v.Valid {
				numericCount++
			}
		case *pgtype.Numeric:
			if v != nil && v.Valid {
				numericCount++
			}
		case time.Time:
			dateCount++
		case string:
			// Try to parse as date
			if _, err := time.Parse("2006-01-02", v); err == nil {
				dateCount++
			} else if _, err := time.Parse(time.RFC3339, v); err == nil {
				dateCount++
			}
		case bool:
			booleanCount++
		}
	}

	total := len(rows) - nullCount
	if total == 0 {
		profile.Type = ColumnTypeText
		return profile
	}

	// Determine type based on majority
	if numericCount > total/2 {
		profile.Type = ColumnTypeNumeric
		profile.IsMeasure = true
	} else if dateCount > total/2 {
		profile.Type = ColumnTypeDate
		profile.IsTimeSeries = true
	} else if booleanCount > total/2 {
		profile.Type = ColumnTypeBoolean
	} else {
		profile.Type = ColumnTypeText
		profile.IsDimension = true
	}

	// Heuristics for measure vs dimension
	if profile.Type == ColumnTypeNumeric {
		// Check if column name suggests it's a measure
		lowerName := toLower(name)
		if contains(lowerName, measureNameSubstrs) {
			profile.IsMeasure = true
		} else if contains(lowerName, dimensionNameSubstrs) {
			profile.IsDimension = true
			profile.IsMeasure = false
		}
	}

	// Heuristics for dimension
	if profile.Type == ColumnTypeText {
		profile.IsDimension = true
	}

	// Heuristics for time series
	if profile.Type == ColumnTypeDate {
		profile.IsTimeSeries = true
	}

	return profile
}

// Helper functions
func toLower(s string) string {
	return strings.ToLower(s)
}

func contains(s string, substrs []string) bool {
	lower := strings.ToLower(s)
	for _, substr := range substrs {
		if strings.Contains(lower, strings.ToLower(substr)) {
			return true
		}
	}
	return false
}

// GetNumericValue extracts a numeric value from an interface{}
func GetNumericValue(val interface{}) (float64, bool) {
	if val == nil {
		return 0, false
	}

	switch v := val.(type) {
	case float64:
		return v, true
	case int64:
		return float64(v), true
	case int32:
		return float64(v), true
	case int:
		return float64(v), true
	case pgtype.Numeric:
		if !v.Valid {
			return 0, false
		}
		f8, err := v.Float64Value()
		if err != nil || !f8.Valid {
			return 0, false
		}
		return f8.Float64, true
	case *pgtype.Numeric:
		if v == nil || !v.Valid {
			return 0, false
		}
		f8, err := v.Float64Value()
		if err != nil || !f8.Valid {
			return 0, false
		}
		return f8.Float64, true
	case string:
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f, true
		}
	}

	return 0, false
}
