package llm

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	emailPattern = regexp.MustCompile(`(?i)[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}`)
	phonePattern = regexp.MustCompile(`\b(?:\+?1[-.\s]?)?(?:\(?\d{3}\)?[-.\s]?){2}\d{4}\b`)
	ssnPattern   = regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`)
)

var sensitiveColumnSubstrings = []string{
	"email", "e_mail", "phone", "mobile", "ssn", "social_security",
	"password", "passwd", "secret", "token", "credit_card", "card_number",
	"iban", "account_number", "birth_date", "dob", "address", "zip_code", "postal",
}

// RedactRows returns a copy of rows with sensitive column values and common PII patterns masked.
func RedactRows(columns []string, rows [][]interface{}) [][]interface{} {
	if len(rows) == 0 {
		return rows
	}
	sensitive := sensitiveColumns(columns)
	out := make([][]interface{}, len(rows))
	for i, row := range rows {
		clone := make([]interface{}, len(row))
		for j, val := range row {
			if j < len(sensitive) && sensitive[j] {
				clone[j] = "[REDACTED]"
				continue
			}
			clone[j] = redactValue(val)
		}
		out[i] = clone
	}
	return out
}

func sensitiveColumns(columns []string) []bool {
	out := make([]bool, len(columns))
	for i, col := range columns {
		lower := strings.ToLower(strings.TrimSpace(col))
		for _, sub := range sensitiveColumnSubstrings {
			if strings.Contains(lower, sub) {
				out[i] = true
				break
			}
		}
	}
	return out
}

func redactValue(v interface{}) interface{} {
	if v == nil {
		return nil
	}
	s := strings.TrimSpace(fmt.Sprint(v))
	if s == "" {
		return v
	}
	if emailPattern.MatchString(s) {
		return "[REDACTED_EMAIL]"
	}
	if phonePattern.MatchString(s) {
		return "[REDACTED_PHONE]"
	}
	if ssnPattern.MatchString(s) {
		return "[REDACTED_SSN]"
	}
	return v
}
