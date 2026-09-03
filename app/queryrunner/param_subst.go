package queryrunner

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// maxBindValueLen bounds a single bind value. Compare/equivalence binds are
// region names, dates, and small numbers; anything larger is rejected rather
// than substituted.
const maxBindValueLen = 4096

// SubstituteParams replaces $1..$n in sql with literal values from binds. It is
// used only to run an executed compare / equivalence check on a parameterized
// candidate — the placeholder form is what gets stored and shown.
//
// Every bind is emitted either as a strict decimal / boolean token or as a
// single-quoted string literal with every embedded quote doubled; a value is
// never emitted bare or cast unless it matches a fully anchored number / date /
// timestamp pattern end to end. Bind values carrying a NUL or a backslash are
// rejected outright (a backslash is an escape when standard_conforming_strings
// is off, so it cannot be quoted portably). The result is re-parsed to confirm
// it is one param-free SELECT, and callers MUST still run it through the
// read-only validator.
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
		if len(b) > maxBindValueLen {
			return "", fmt.Errorf("bind $%d is %d bytes; limit is %d", i+1, len(b), maxBindValueLen)
		}
		if strings.ContainsRune(b, 0) {
			return "", fmt.Errorf("bind $%d contains a NUL byte", i+1)
		}
		if strings.ContainsRune(b, '\\') {
			return "", fmt.Errorf("bind $%d contains a backslash, which cannot be substituted safely", i+1)
		}
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
	// All three are anchored end to end (`^...$`): a bind takes the bare-number
	// or date/timestamp-cast path only when it is *entirely* that shape, never
	// when it merely starts like one (e.g. `2025-01-01T00:00:00'::timestamp OR 1=1 --`).
	decimalRe   = regexp.MustCompile(`^[+-]?\d+(\.\d+)?([eE][+-]?\d+)?$`)
	dateOnlyRe  = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	timestampRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}[ T]\d{2}:\d{2}(:\d{2}(\.\d{1,6})?)?([+-]\d{2}(:?\d{2})?|Z)?$`)
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

// bindLiteral renders a bind value as a SQL literal. A value that is *entirely* a
// strict decimal number or boolean is emitted bare; one that is *entirely* an
// ISO date or timestamp is emitted as a quoted, quote-escaped literal with an
// explicit cast so date arithmetic in the rewrite (`$1 + INTERVAL '1 month'`)
// type-checks; everything else — including a value that only looks numeric or
// temporal at the front — becomes a single-quoted string with every embedded
// quote doubled. `strconv.ParseFloat` is deliberately not used: it accepts
// `NaN`, `Inf`, hex floats, and digit separators, none of which are safe to emit
// unquoted.
func bindLiteral(v string) string {
	s := strings.TrimSpace(v)
	switch {
	case s == "":
		return "''"
	case decimalRe.MatchString(s):
		return s
	case strings.EqualFold(s, "true"):
		return "true"
	case strings.EqualFold(s, "false"):
		return "false"
	case dateOnlyRe.MatchString(s):
		return quoteSQLLiteral(s) + "::date"
	case timestampRe.MatchString(s):
		return quoteSQLLiteral(s) + "::timestamp"
	default:
		return quoteSQLLiteral(s)
	}
}

// quoteSQLLiteral renders s as a single-quoted SQL string literal with every
// embedded single quote doubled. Callers reject bind values containing a
// backslash or NUL, so this is safe regardless of standard_conforming_strings.
func quoteSQLLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
