// Package format provides shared formatting helpers used across packages.
package format

import (
	"fmt"
	"strings"
)

// FloatWithCommas formats a float64 with thousands separators (e.g. 45793291.51 -> "45,793,291.51").
func FloatWithCommas(v float64) string {
	s := fmt.Sprintf("%.2f", v)
	idx := strings.Index(s, ".")
	if idx == -1 {
		idx = len(s)
	}
	integerPart := s[:idx]
	var b strings.Builder
	for i, c := range integerPart {
		if i > 0 && (len(integerPart)-i)%3 == 0 {
			b.WriteString(",")
		}
		b.WriteRune(c)
	}
	if idx < len(s) {
		b.WriteString(s[idx:])
	}
	return b.String()
}

// StrPtr returns a pointer to s. Avoids inline &s which doesn't compile for string literals.
func StrPtr(s string) *string {
	return &s
}
