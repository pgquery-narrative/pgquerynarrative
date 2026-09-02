package web

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/pgquerynarrative/pgquerynarrative/api/gen/reports"
)

// fetchReport loads the report named by the ?id= query param, or writes an HTTP
// error and returns nil.
func (h *Handlers) fetchReport(w http.ResponseWriter, r *http.Request) (*reports.Report, string) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return nil, ""
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "missing report id", http.StatusBadRequest)
		return nil, ""
	}
	got, err := h.reportsEndpoints.Get(r.Context(), &reports.GetPayload{ID: id})
	if err != nil {
		if _, ok := err.(*reports.NotFoundError); ok {
			http.Error(w, "report not found", http.StatusNotFound)
			return nil, ""
		}
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return nil, ""
	}
	report, ok := got.(*reports.Report)
	if !ok {
		http.Error(w, "invalid report", http.StatusInternalServerError)
		return nil, ""
	}
	short := id
	if len(short) > 8 {
		short = short[:8]
	}
	return report, short
}

// ExportReportJSON serves the full report object as a JSON file.
func (h *Handlers) ExportReportJSON(w http.ResponseWriter, r *http.Request) {
	report, short := h.fetchReport(w, r)
	if report == nil {
		return
	}
	body, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		http.Error(w, "encode failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="report-`+short+`.json"`)
	_, _ = w.Write(body)
}

// ExportReportMarkdown serves the report as a Markdown file — the shape a person
// pastes into a PR description or an incident doc.
func (h *Handlers) ExportReportMarkdown(w http.ResponseWriter, r *http.Request) {
	report, short := h.fetchReport(w, r)
	if report == nil {
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="report-`+short+`.md"`)
	_, _ = io.WriteString(w, buildReportMarkdown(report))
}

// ExportReportSQL serves a .sql file with the before/after statements and any
// candidate index DDL, ready to drop into a migration or a PR.
func (h *Handlers) ExportReportSQL(w http.ResponseWriter, r *http.Request) {
	report, short := h.fetchReport(w, r)
	if report == nil {
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="report-`+short+`.sql"`)
	_, _ = io.WriteString(w, buildReportSQL(report))
}

func buildReportMarkdown(report *reports.Report) string {
	var b strings.Builder
	inv := investigationMap(report)

	title := "Query report"
	if report.Narrative != nil && report.Narrative.Headline != "" {
		title = report.Narrative.Headline
	}
	fmt.Fprintf(&b, "# %s\n\n", title)
	if report.CreatedAt != "" {
		fmt.Fprintf(&b, "_Generated %s", report.CreatedAt)
		if report.LlmProvider != "" || report.LlmModel != "" {
			fmt.Fprintf(&b, " · %s / %s", report.LlmProvider, report.LlmModel)
		}
		b.WriteString("_\n\n")
	}

	if inv != nil {
		if s := mapString(inv, "executive_summary"); s != "" {
			fmt.Fprintf(&b, "## Executive summary\n\n%s\n\n", s)
		}
		if impact := mapAny(inv, "impact"); impact != nil {
			b.WriteString("## Impact\n\n")
			if sev := mapString(impact, "severity"); sev != "" {
				fmt.Fprintf(&b, "- **Severity:** %s\n", sev)
			}
			if s := mapString(impact, "summary"); s != "" {
				fmt.Fprintf(&b, "- %s\n", s)
			}
			b.WriteString("\n")
		}
		if ev := mapStrings(inv, "postgresql_evidence"); len(ev) > 0 {
			b.WriteString("## PostgreSQL evidence\n\n")
			for _, e := range ev {
				fmt.Fprintf(&b, "- %s\n", e)
			}
			b.WriteString("\n")
		}
		if findings := collapseInvestigationFindings(mapSlice(inv, "plan_findings")); len(findings) > 0 {
			b.WriteString("## Execution-plan findings\n\n")
			for _, f := range findings {
				line := mapString(f, "message")
				if cat := mapString(f, "category"); cat != "" {
					line = "**" + cat + ":** " + line
				}
				fmt.Fprintf(&b, "- %s\n", line)
			}
			b.WriteString("\n")
		}
		if cands := mapSlice(inv, "candidate_improvements"); len(cands) > 0 {
			b.WriteString("## Candidate improvements\n\n")
			for i, c := range cands {
				if i >= 3 {
					break
				}
				if sql := strings.TrimSpace(mapString(c, "proposed_change")); sql != "" {
					fmt.Fprintf(&b, "```sql\n%s\n```\n\n", sql)
				}
				if why := mapString(c, "why_it_might_help"); why != "" {
					fmt.Fprintf(&b, "%s\n\n", why)
				}
			}
		}
		if eq := mapAny(inv, "equivalence_validation"); eq != nil {
			status := mapString(eq, "status")
			if status == "" {
				status = "Unverified"
			}
			fmt.Fprintf(&b, "## Result equivalence: %s\n\n", status)
			if notes := mapString(eq, "notes"); notes != "" {
				fmt.Fprintf(&b, "%s\n\n", notes)
			}
		}
		if next := mapString(inv, "recommended_next_action"); next != "" {
			fmt.Fprintf(&b, "## Recommended next action\n\n%s\n\n", next)
		}
		writeMarkdownList(&b, "Risks and tradeoffs", mapStrings(inv, "risks_and_tradeoffs"))
	} else if report.Narrative != nil {
		writeMarkdownList(&b, "Key takeaways", report.Narrative.Takeaways)
		writeMarkdownList(&b, "Drivers", report.Narrative.Drivers)
		writeMarkdownList(&b, "Limitations", report.Narrative.Limitations)
		writeMarkdownList(&b, "Recommendations", report.Narrative.Recommendations)
	}

	if report.SQL != "" {
		fmt.Fprintf(&b, "## Query\n\n```sql\n%s\n```\n", strings.TrimSpace(report.SQL))
	}
	return b.String()
}

func writeMarkdownList(b *strings.Builder, heading string, items []string) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(b, "## %s\n\n", heading)
	for _, it := range items {
		fmt.Fprintf(b, "- %s\n", it)
	}
	b.WriteString("\n")
}

func buildReportSQL(report *reports.Report) string {
	var b strings.Builder
	inv := investigationMap(report)
	shortID := report.ID
	if len(shortID) > 8 {
		shortID = shortID[:8]
	}
	fmt.Fprintf(&b, "-- PgQueryNarrative report %s\n", shortID)
	if inv != nil {
		if s := mapString(inv, "executive_summary"); s != "" {
			for _, line := range strings.Split(s, "\n") {
				fmt.Fprintf(&b, "-- %s\n", line)
			}
		}
		b.WriteString("\n-- BEFORE (original)\n")
		fmt.Fprintf(&b, "%s;\n", strings.TrimSpace(firstNonEmpty(mapString(inv, "source_query"), report.SQL)))

		var rewrites, indexes []string
		for _, c := range mapSlice(inv, "candidate_improvements") {
			sql := strings.TrimSpace(mapString(c, "proposed_change"))
			if sql == "" {
				continue
			}
			head := strings.ToLower(strings.TrimSpace(sql))
			if strings.HasPrefix(head, "create ") && strings.Contains(head, "index") {
				indexes = append(indexes, sql)
			} else {
				rewrites = append(rewrites, sql)
			}
		}
		for i, sql := range rewrites {
			fmt.Fprintf(&b, "\n-- AFTER (candidate rewrite %d)\n%s;\n", i+1, strings.TrimSuffix(sql, ";"))
		}
		for _, ddl := range indexes {
			fmt.Fprintf(&b, "\n-- Candidate index — for review only, never auto-applied\n%s;\n", strings.TrimSuffix(ddl, ";"))
		}
		if eq := mapAny(inv, "equivalence_validation"); eq != nil {
			if status := mapString(eq, "status"); status != "" {
				fmt.Fprintf(&b, "\n-- Result equivalence: %s\n", status)
			}
		}
		return b.String()
	}

	fmt.Fprintf(&b, "\n%s;\n", strings.TrimSpace(report.SQL))
	return b.String()
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
