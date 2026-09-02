package queryrunner

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// SubstituteParams replaces $1..$n in sql with literal values from binds. It is
// used only to run an executed compare / equivalence check on a parameterized
// candidate — the placeholder form is what gets stored and shown.
//
// The substitution is text-level but SQL-aware (single-quoted strings and line
// comments are skipped) and every result is re-parsed to confirm no ParamRef
// survived. Callers MUST still run the output through the read-only validator.
func SubstituteParams(sql string, binds []string) (string, error) {
	trimmed := trimSQL(sql)
	n := maxParamNumber(trimmed)
	if n == 0 {
		return trimmed, nil
	}
	if len(binds) < n {
		return "", fmt.Errorf("query uses $%d but only %d bind value(s) supplied", n, len(binds))
	}
	if strings.Contains(trimmed, "$$") || dollarQuoteTagRe.MatchString(trimmed) {
		return "", fmt.Errorf("dollar-quoted strings are not supported for bind substitution")
	}

	lits := make([]string, len(binds))
	for i, b := range binds {
		lits[i] = bindLiteral(b)
	}

	out := replaceDollarParams(trimmed, lits)

	// Confirm the substitution produced valid, param-free SQL.
	_, sel, ok := parseSingleSelect(out)
	if !ok {
		return "", fmt.Errorf("bind substitution produced invalid SQL")
	}
	if selectTreeContainsParamRef(sel) {
		return "", fmt.Errorf("bind substitution left unresolved parameters")
	}
	return out, nil
}

var (
	paramRefLexRe    = regexp.MustCompile(`\$[1-9]\d*`)
	dollarQuoteTagRe = regexp.MustCompile(`\$[A-Za-z_]\w*\$`)
	dateOnlyRe       = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	timestampRe      = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}[ T]\d{2}:\d{2}(:\d{2})?`)
)

// maxParamNumber returns the highest $N referenced anywhere in sql (lexical).
func maxParamNumber(sql string) int {
	max := 0
	for _, m := range paramRefLexRe.FindAllString(stripQuotedAndComments(sql), -1) {
		if v, err := strconv.Atoi(m[1:]); err == nil && v > max {
			max = v
		}
	}
	return max
}

// replaceDollarParams swaps $N tokens for lits[N-1], skipping single-quoted
// strings and -- line comments.
func replaceDollarParams(sql string, lits []string) string {
	var b strings.Builder
	r := []rune(sql)
	for i := 0; i < len(r); {
		switch {
		case r[i] == '\'':
			b.WriteRune(r[i])
			i++
			for i < len(r) {
				b.WriteRune(r[i])
				if r[i] == '\'' {
					if i+1 < len(r) && r[i+1] == '\'' {
						b.WriteRune(r[i+1])
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
		case r[i] == '-' && i+1 < len(r) && r[i+1] == '-':
			for i < len(r) && r[i] != '\n' {
				b.WriteRune(r[i])
				i++
			}
		case r[i] == '$' && i+1 < len(r) && r[i+1] >= '1' && r[i+1] <= '9':
			j := i + 1
			for j < len(r) && r[j] >= '0' && r[j] <= '9' {
				j++
			}
			num, _ := strconv.Atoi(string(r[i+1 : j]))
			if num >= 1 && num <= len(lits) {
				b.WriteString(lits[num-1])
			} else {
				b.WriteString(string(r[i:j]))
			}
			i = j
		default:
			b.WriteRune(r[i])
			i++
		}
	}
	return b.String()
}

// stripQuotedAndComments blanks single-quoted strings and -- comments so a
// lexical scan for $N doesn't see placeholders that are really string content.
func stripQuotedAndComments(sql string) string {
	var b strings.Builder
	r := []rune(sql)
	for i := 0; i < len(r); {
		switch {
		case r[i] == '\'':
			i++
			for i < len(r) {
				if r[i] == '\'' {
					if i+1 < len(r) && r[i+1] == '\'' {
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
		case r[i] == '-' && i+1 < len(r) && r[i+1] == '-':
			for i < len(r) && r[i] != '\n' {
				i++
			}
		default:
			b.WriteRune(r[i])
			i++
		}
	}
	return b.String()
}

// bindLiteral renders a bind value as a SQL literal. Numbers and booleans go
// bare; date/timestamp-shaped strings get an explicit cast so date arithmetic in
// the rewrite (`$1 + INTERVAL '1 month'`) type-checks; everything else is a
// single-quoted string with quotes escaped.
func bindLiteral(v string) string {
	s := strings.TrimSpace(v)
	if s == "" {
		return "''"
	}
	if _, err := strconv.ParseInt(s, 10, 64); err == nil {
		return s
	}
	if _, err := strconv.ParseFloat(s, 64); err == nil {
		return s
	}
	if s == "true" || s == "false" || s == "TRUE" || s == "FALSE" {
		return strings.ToLower(s)
	}
	if dateOnlyRe.MatchString(s) {
		return "'" + s + "'::date"
	}
	if timestampRe.MatchString(s) {
		return "'" + s + "'::timestamp"
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
