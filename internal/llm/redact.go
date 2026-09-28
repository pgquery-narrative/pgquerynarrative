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
	// UK national insurance: AB123456C
	ukNIPattern = regexp.MustCompile(`\b[A-Z]{2}\d{6}[A-D]\b`)
	// E.164-ish international numbers (7–15 digits with optional + prefix).
	intlPhonePattern = regexp.MustCompile(`\+?\d[\d\s\-().]{6,18}\d`)
	ibanPattern      = regexp.MustCompile(`\b[A-Z]{2}\d{2}[A-Z0-9]{11,30}\b`)
	sqlStringLiteral = regexp.MustCompile(`'([^']|'')*'`)
	sqlLineComment   = regexp.MustCompile(`--[^\n]*`)
	sqlBlockComment  = regexp.MustCompile(`/\*[\s\S]*?\*/`)
)

var sensitiveColumnSubstrings = []string{
	"email", "e_mail", "phone", "mobile", "ssn", "social_security",
	"password", "passwd", "secret", "token", "credit_card", "card_number",
	"iban", "account_number", "birth_date", "dob", "address", "zip_code", "postal",
	"national_insurance", "passport", "driver_license",
}

var promptInjectionNeedles = []string{
	"ignore previous instructions",
	"ignore all previous",
	"disregard the above",
	"you are now",
	"system:",
	"assistant:",
	"new instructions:",
	"override safety",
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

// RedactSQL masks single-quoted string literals before SQL text is sent to an LLM.
func RedactSQL(sql string) string {
	sql = strings.TrimSpace(sql)
	if sql == "" {
		return sql
	}
	return sqlStringLiteral.ReplaceAllString(sql, "'[REDACTED]'")
}

// StripSQLComments removes line and block comments from SQL (reduces injection via comments).
func StripSQLComments(sql string) string {
	sql = sqlLineComment.ReplaceAllString(sql, "")
	sql = sqlBlockComment.ReplaceAllString(sql, " ")
	return strings.TrimSpace(sql)
}

// SanitizeRAGContext neutralizes instruction-like content in retrieved similar-query context.
func SanitizeRAGContext(context string) string {
	if strings.TrimSpace(context) == "" {
		return ""
	}
	lines := strings.Split(context, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if ContainsPromptInjection(line) {
			out = append(out, "- [REDACTED_UNTRUSTED_CONTEXT]")
			continue
		}
		if s, ok := redactValue(line).(string); ok {
			out = append(out, s)
		} else {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// ContainsPromptInjection reports common instruction-override patterns in untrusted text.
func ContainsPromptInjection(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" {
		return false
	}
	for _, needle := range promptInjectionNeedles {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

// PrepareSQLForPrompt returns SQL safe to embed in an LLM prompt when redaction is enabled.
func PrepareSQLForPrompt(sql string, redactPII bool) string {
	if ContainsPromptInjection(sql) {
		return "[REDACTED_QUERY_INJECTION]"
	}
	sql = StripSQLComments(sql)
	if redactPII {
		return RedactSQL(sql)
	}
	return sql
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
	if ssnPattern.MatchString(s) {
		return "[REDACTED_SSN]"
	}
	if ukNIPattern.MatchString(s) {
		return "[REDACTED_NI]"
	}
	if ibanPattern.MatchString(s) {
		return "[REDACTED_IBAN]"
	}
	if phonePattern.MatchString(s) || intlPhonePattern.MatchString(s) {
		return "[REDACTED_PHONE]"
	}
	return v
}
