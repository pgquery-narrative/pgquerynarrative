package pqncli

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"
)

// clip shortens s to at most n runes, marking the cut.
func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// wrap breaks s into lines of at most width runes at spaces.
func wrap(s string, width int) []string {
	var lines []string
	line := ""
	for _, w := range strings.Fields(s) {
		if line != "" && utf8.RuneCountInString(line)+1+utf8.RuneCountInString(w) > width {
			lines = append(lines, line)
			line = w
			continue
		}
		if line == "" {
			line = w
		} else {
			line += " " + w
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}

func printWrapped(w io.Writer, indent, s string, width int) {
	for _, l := range wrap(s, width) {
		fmt.Fprintf(w, "%s%s\n", indent, l)
	}
}

// Table prints rows in aligned columns. Cells are clipped to maxCell runes.
func Table(w io.Writer, header []string, rows [][]string, maxCell int) {
	width := make([]int, len(header))
	for i, h := range header {
		width[i] = utf8.RuneCountInString(h)
	}
	cells := make([][]string, len(rows))
	for i, r := range rows {
		cells[i] = make([]string, len(header))
		for j := range header {
			v := ""
			if j < len(r) {
				v = clip(r[j], maxCell)
			}
			cells[i][j] = v
			if n := utf8.RuneCountInString(v); n > width[j] {
				width[j] = n
			}
		}
	}
	line := func(vals []string) {
		var b strings.Builder
		for j, v := range vals {
			b.WriteString(v)
			if j < len(vals)-1 {
				b.WriteString(strings.Repeat(" ", width[j]-utf8.RuneCountInString(v)+2))
			}
		}
		fmt.Fprintln(w, strings.TrimRight(b.String(), " "))
	}
	line(header)
	sep := make([]string, len(header))
	for j := range header {
		sep[j] = strings.Repeat("-", width[j])
	}
	line(sep)
	for _, r := range cells {
		line(r)
	}
}

func ms(v float64) string {
	switch {
	case v == 0:
		return "-"
	case v >= 1000:
		return fmt.Sprintf("%.2f s", v/1000)
	default:
		return fmt.Sprintf("%.1f ms", v)
	}
}

// Render prints an investigation as text for a terminal.
func (r *Report) Render(w io.Writer) {
	const width = 96
	if r.InvestigationID > 0 {
		fmt.Fprintf(w, "PgQueryNarrative investigation #%d\n", r.InvestigationID)
	} else {
		fmt.Fprintln(w, "PgQueryNarrative investigation (not recorded)")
	}
	fmt.Fprintln(w, strings.Repeat("=", 60))

	fmt.Fprintln(w, "\nStatement")
	printWrapped(w, "  ", r.SQL, width)
	if r.ExecutedSQL != "" {
		fmt.Fprintln(w, "  with the --bind values substituted:")
		printWrapped(w, "  ", r.ExecutedSQL, width)
	}
	kind := "estimated plan"
	if r.GenericPlan {
		kind = "generic plan (placeholders)"
	}
	fmt.Fprintf(w, "  %s, total cost %.0f\n", kind, r.TotalCost)

	fmt.Fprintln(w, "\nWhat the plan shows")
	if len(r.Findings) == 0 {
		fmt.Fprintln(w, "  Nothing the rules flag.")
	}
	for _, f := range r.Findings {
		fmt.Fprintf(w, "  [%s] %s", f.Confidence, f.Category)
		if f.Relation != "" {
			fmt.Fprintf(w, " on %s", f.Relation)
		}
		fmt.Fprintln(w)
		printWrapped(w, "      ", f.Message, width-6)
		for i, e := range f.Evidence {
			if i == 3 {
				break
			}
			printWrapped(w, "      - ", e, width-8)
		}
	}

	var rewrites, indexes []CandidateResult
	for _, c := range r.Candidates {
		if c.Kind == "index_ddl" {
			indexes = append(indexes, c)
		} else {
			rewrites = append(rewrites, c)
		}
	}

	fmt.Fprintln(w, "\nProposed from the plan")
	if len(rewrites) == 0 && len(indexes) == 0 {
		fmt.Fprintln(w, "  Nothing to propose.")
	}
	for i, c := range rewrites {
		fmt.Fprintf(w, "  %d. %s (confidence %s)\n", i+1, c.Category, c.Confidence)
		printWrapped(w, "     ", c.Rationale, width-5)
		fmt.Fprintln(w, "     rewrite:")
		printWrapped(w, "       ", c.SQL, width-7)
	}
	for _, c := range indexes {
		fmt.Fprintln(w, "  index:")
		for _, l := range strings.Split(c.DDL, "\n") {
			if strings.HasPrefix(strings.TrimSpace(l), "--") {
				printWrapped(w, "     ", l, width-5)
			} else if strings.TrimSpace(l) != "" {
				fmt.Fprintf(w, "     %s\n", strings.TrimSpace(l))
			}
		}
		printWrapped(w, "     ", c.Rationale, width-5)
	}

	if len(rewrites) > 0 {
		fmt.Fprintln(w, "\nProof (same rows, then faster)")
		rows := make([][]string, 0, len(rewrites))
		for i, c := range rewrites {
			rowsCell, before, after, speed := "-", "-", "-", "-"
			if c.AfterMs > 0 || c.BeforeMs > 0 {
				rowsCell = fmt.Sprintf("%d", c.Rows)
				before, after = ms(c.BeforeMs), ms(c.AfterMs)
				speed = fmt.Sprintf("%.1fx", c.Speedup)
			}
			rows = append(rows, []string{
				fmt.Sprintf("%d", i+1), c.Category, rowsCell, before, after, speed,
				fmt.Sprintf("%.0f -> %.0f", c.CostBefore, c.CostAfter), c.Verdict,
			})
		}
		Table(w, []string{"#", "change", "rows", "before", "after", "speedup", "est. cost", "verdict"}, rows, 28)
		for i, c := range rewrites {
			fmt.Fprintf(w, "  %d: %s", i+1, c.Reason)
			if c.ProofSource != "" {
				fmt.Fprintf(w, " (%s)", c.ProofSource)
			}
			fmt.Fprintln(w)
		}
	}

	fmt.Fprintln(w, "\nVerdict")
	counts := map[string]int{}
	for _, c := range rewrites {
		counts[c.Verdict]++
	}
	switch {
	case r.Proven > 0:
		fmt.Fprintf(w, "  %d proposal(s) proven: same rows and at least %.1fx faster. A person still decides what to deploy.\n", r.Proven, minProvenSpeedup)
	case counts[VerdictDifferent] > 0:
		fmt.Fprintf(w, "  Nothing proven. %d proposal(s) returned different rows and must not be used.\n", counts[VerdictDifferent])
	case len(rewrites) > 0:
		fmt.Fprintln(w, "  Nothing proven. The proposals were not shown to be faster on the same rows.")
	default:
		fmt.Fprintln(w, "  Nothing proven, because there was nothing to measure.")
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		var parts []string
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s %d", k, counts[k]))
		}
		fmt.Fprintf(w, "  (%s)\n", strings.Join(parts, ", "))
	}
	for _, n := range r.Notes {
		for i, l := range wrap(n, width-8) {
			prefix := "  note: "
			if i > 0 {
				prefix = "        "
			}
			fmt.Fprintf(w, "%s%s\n", prefix, l)
		}
	}
	if r.InvestigationID > 0 {
		fmt.Fprintf(w, "\nEvidence is in the ledger: pqn evidence %d\n", r.InvestigationID)
	}
}
