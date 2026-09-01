// Package web provides HTTP handlers and PDF export for the PgQueryNarrative UI.
// pdf.go builds well-structured PDF reports from the report type.
package web

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/jung-kurt/gofpdf/v2"
	"github.com/pgquerynarrative/pgquerynarrative/api/gen/reports"
	"github.com/pgquerynarrative/pgquerynarrative/app/queryrunner"
)

// BuildReportPDF writes a structured PDF report to w. Uses Helvetica (ASCII-safe).
func BuildReportPDF(w io.Writer, report *reports.Report) error {
	pdf := gofpdf.New("P", "pt", "A4", "")
	pdf.SetCreator("PgQueryNarrative", false)
	pdf.SetProducer("PgQueryNarrative", false)
	pdf.SetTitle("PgQueryNarrative Report", false)
	pdf.SetMargins(40, 40, 40)
	pdf.SetAutoPageBreak(true, 28)
	pdf.AddPage()
	pdf.SetFont("Helvetica", "", 10)

	// Title and meta
	pdf.SetFont("Helvetica", "B", 16)
	pdf.CellFormat(0, 16, "PgQueryNarrative Report", "", 1, "L", false, 0, "")
	pdf.SetFont("Helvetica", "", 9)
	pdf.SetTextColor(100, 100, 100)
	pdf.CellFormat(0, 10, "Generated: "+report.CreatedAt, "", 1, "L", false, 0, "")
	if report.LlmProvider != "" || report.LlmModel != "" {
		model := report.LlmProvider
		if report.LlmModel != "" {
			if model != "" {
				model += " / "
			}
			model += report.LlmModel
		}
		pdf.CellFormat(0, 10, "Model: "+model, "", 1, "L", false, 0, "")
	}
	pdf.SetTextColor(0, 0, 0)
	pdf.Ln(4)

	// Query (collapsed in a box)
	sqlStr := report.SQL
	if len(sqlStr) > 400 {
		sqlStr = sqlStr[:400] + "..."
	}
	pdf.SetFont("Courier", "", 8)
	pdf.SetFillColor(245, 245, 245)
	pdf.MultiCell(0, 12, safePDFString(sqlStr), "1", "L", true)
	pdf.SetFont("Helvetica", "", 10)
	pdf.Ln(8)

	if report.Metrics != nil && report.Metrics.Investigation != nil {
		writeInvestigationPDF(pdf, report)
		return pdf.Output(w)
	}

	// Narrative: headline and sections
	if report.Narrative != nil {
		if report.Narrative.Headline != "" {
			pdf.SetFont("Helvetica", "B", 14)
			pdf.MultiCell(0, 14, safePDFString(report.Narrative.Headline), "", "L", false)
			pdf.SetFont("Helvetica", "", 10)
			pdf.Ln(6)
		}
		if len(report.Narrative.Takeaways) > 0 {
			sectionTitle(pdf, "Key takeaways")
			for _, t := range report.Narrative.Takeaways {
				pdf.CellFormat(12, 10, "", "", 0, "L", false, 0, "")
				pdf.MultiCell(0, 10, safePDFString(t), "", "L", false)
			}
			pdf.Ln(4)
		}
		for _, title := range []string{"Drivers", "Limitations", "Recommendations"} {
			var items []string
			switch title {
			case "Drivers":
				items = queryrunner.CollapseRepeatedFindingMessages(report.Narrative.Drivers)
				if len(items) > 8 {
					omitted := len(items) - 8
					items = append(append([]string{}, items[:8]...), fmt.Sprintf("...and %d more similar items omitted", omitted))
				}
			case "Limitations":
				items = report.Narrative.Limitations
			case "Recommendations":
				items = report.Narrative.Recommendations
			}
			if len(items) > 0 {
				sectionTitle(pdf, title)
				for _, s := range items {
					pdf.CellFormat(12, 10, "", "", 0, "L", false, 0, "")
					pdf.MultiCell(0, 10, safePDFString(s), "", "L", false)
				}
				pdf.Ln(4)
			}
		}
	}

	// Metrics bar chart (when we have time series data)
	if report.Metrics != nil && len(report.Metrics.TimeSeries) > 0 {
		drawMetricsBarChart(pdf, report.Metrics.TimeSeries)
		pdf.Ln(8)
	}

	// Chart suggestions
	if len(report.ChartSuggestions) > 0 {
		sectionTitle(pdf, "Suggested charts")
		for _, s := range report.ChartSuggestions {
			if s == nil {
				continue
			}
			pdf.CellFormat(12, 10, "", "", 0, "L", false, 0, "")
			pdf.SetFont("Helvetica", "B", 10)
			pdf.CellFormat(0, 10, safePDFString(s.Label), "", 0, "L", false, 0, "")
			pdf.SetFont("Helvetica", "", 10)
			pdf.Ln(10)
			pdf.CellFormat(20, 10, "", "", 0, "L", false, 0, "")
			pdf.MultiCell(0, 10, safePDFString(s.Reason), "", "L", false)
		}
		pdf.Ln(4)
	}

	// Time series / period comparison
	if report.Metrics != nil && len(report.Metrics.TimeSeries) > 0 {
		sectionTitle(pdf, "Vs previous period")
		measures := make([]string, 0, len(report.Metrics.TimeSeries))
		for m := range report.Metrics.TimeSeries {
			measures = append(measures, m)
		}
		sort.Strings(measures)
		for _, measure := range measures {
			ts := report.Metrics.TimeSeries[measure]
			if ts == nil {
				continue
			}
			line := fmt.Sprintf("%s: %.2f", measure, ts.CurrentPeriod)
			if ts.PreviousPeriod != nil {
				line += fmt.Sprintf(" (prev: %.2f)", *ts.PreviousPeriod)
			}
			if ts.ChangePercentage != nil {
				line += fmt.Sprintf(" %+.1f%%", *ts.ChangePercentage)
			}
			line += " " + ts.Trend
			pdf.CellFormat(12, 10, "", "", 0, "L", false, 0, "")
			pdf.MultiCell(0, 10, line, "", "L", false)
		}
		pdf.Ln(4)
	}

	// Cohorts
	if report.Metrics != nil && len(report.Metrics.Cohorts) > 0 {
		sectionTitle(pdf, "Cohorts")
		for _, c := range report.Metrics.Cohorts {
			if c == nil {
				continue
			}
			pdf.CellFormat(12, 10, "", "", 0, "L", false, 0, "")
			pdf.SetFont("Helvetica", "B", 10)
			pdf.MultiCell(0, 10, safePDFString(c.CohortLabel), "", "L", false)
			pdf.SetFont("Helvetica", "", 10)
			if c.RetentionPct != nil {
				pdf.CellFormat(20, 10, "", "", 0, "L", false, 0, "")
				pdf.MultiCell(0, 10, fmt.Sprintf("Retention: %.1f%%", *c.RetentionPct), "", "L", false)
			}
			if len(c.Periods) > 0 {
				w0, w1 := 60.0, 80.0
				pdf.SetFont("Helvetica", "B", 9)
				pdf.CellFormat(w0, 10, "Period", "1", 0, "L", true, 0, "")
				pdf.CellFormat(w1, 10, "Value", "1", 1, "R", true, 0, "")
				pdf.SetFont("Helvetica", "", 9)
				for _, p := range c.Periods {
					if p == nil {
						continue
					}
					pdf.CellFormat(w0, 10, safePDFString(p.PeriodLabel), "1", 0, "L", false, 0, "")
					pdf.CellFormat(w1, 10, fmt.Sprintf("%.2f", p.Value), "1", 1, "R", false, 0, "")
				}
				pdf.Ln(2)
			}
		}
		pdf.Ln(4)
	}

	// Data quality table
	if report.Metrics != nil && len(report.Metrics.DataQuality) > 0 {
		sectionTitle(pdf, "Data quality")
		cols := make([]string, 0, len(report.Metrics.DataQuality))
		for c := range report.Metrics.DataQuality {
			cols = append(cols, c)
		}
		sort.Strings(cols)
		// Table header
		w0, w1, w2, w3, w4 := 80.0, 45.0, 50.0, 55.0, 45.0
		pdf.SetFont("Helvetica", "B", 9)
		pdf.CellFormat(w0, 10, "Column", "1", 0, "L", true, 0, "")
		pdf.CellFormat(w1, 10, "Nulls", "1", 0, "R", true, 0, "")
		pdf.CellFormat(w2, 10, "Null %", "1", 0, "R", true, 0, "")
		pdf.CellFormat(w3, 10, "Distinct", "1", 0, "R", true, 0, "")
		pdf.CellFormat(w4, 10, "Rows", "1", 1, "R", true, 0, "")
		pdf.SetFont("Helvetica", "", 9)
		for _, col := range cols {
			q := report.Metrics.DataQuality[col]
			if q == nil {
				continue
			}
			pdf.CellFormat(w0, 10, safePDFString(col), "1", 0, "L", false, 0, "")
			pdf.CellFormat(w1, 10, fmt.Sprintf("%d", q.NullCount), "1", 0, "R", false, 0, "")
			pdf.CellFormat(w2, 10, fmt.Sprintf("%.1f", q.NullPct), "1", 0, "R", false, 0, "")
			pdf.CellFormat(w3, 10, fmt.Sprintf("%d", q.DistinctCount), "1", 0, "R", false, 0, "")
			pdf.CellFormat(w4, 10, fmt.Sprintf("%d", q.TotalRows), "1", 1, "R", false, 0, "")
		}
		pdf.Ln(4)
	}

	// Performance suggestions
	if report.Metrics != nil && len(report.Metrics.PerfSuggestions) > 0 {
		sectionTitle(pdf, "Performance suggestions")
		for _, s := range report.Metrics.PerfSuggestions {
			pdf.CellFormat(12, 10, "", "", 0, "L", false, 0, "")
			pdf.MultiCell(0, 10, safePDFString(s), "", "L", false)
		}
	}

	return pdf.Output(w)
}

func investigationMap(report *reports.Report) map[string]any {
	if report.Metrics == nil || report.Metrics.Investigation == nil {
		return nil
	}
	if m, ok := report.Metrics.Investigation.(map[string]any); ok {
		return m
	}
	raw, err := json.Marshal(report.Metrics.Investigation)
	if err != nil {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return m
}

func writeInvestigationPDF(pdf *gofpdf.Fpdf, report *reports.Report) {
	inv := investigationMap(report)
	if inv == nil {
		return
	}

	if report.Narrative != nil && report.Narrative.Headline != "" {
		pdf.SetFont("Helvetica", "B", 14)
		pdf.MultiCell(0, 14, safePDFString(report.Narrative.Headline), "", "L", false)
		pdf.SetFont("Helvetica", "", 10)
		pdf.Ln(4)
	}

	findings := collapseInvestigationFindings(mapSlice(inv, "plan_findings"))

	if summary := mapString(inv, "executive_summary"); summary != "" {
		raw := len(mapSlice(inv, "plan_findings"))
		distinct := len(findings)
		if raw > distinct && distinct > 0 {
			summary = strings.Replace(summary,
				fmt.Sprintf("identified %d execution-plan finding(s)", raw),
				fmt.Sprintf("identified %d distinct plan issue(s) (%d partition-level findings)", distinct, raw),
				1)
		}
		sectionTitle(pdf, "Executive summary")
		pdf.MultiCell(0, 10, safePDFString(summary), "", "L", false)
		pdf.Ln(4)
	}

	if impact := mapAny(inv, "impact"); impact != nil {
		sectionTitle(pdf, "Impact")
		if sev := mapString(impact, "severity"); sev != "" {
			pdf.MultiCell(0, 10, safePDFString("Severity: "+sev), "", "L", false)
		}
		if s := mapString(impact, "summary"); s != "" {
			pdf.MultiCell(0, 10, safePDFString(s), "", "L", false)
		}
		pdf.Ln(4)
	}

	if len(findings) > 0 {
		sectionTitle(pdf, "Execution-plan findings")
		const maxFindingsInPDF = 8
		for i, f := range findings {
			if i >= maxFindingsInPDF {
				pdf.CellFormat(12, 10, "", "", 0, "L", false, 0, "")
				pdf.MultiCell(0, 10, safePDFString(fmt.Sprintf("...and %d more finding(s) omitted", len(findings)-maxFindingsInPDF)), "", "L", false)
				break
			}
			pdf.CellFormat(12, 10, "", "", 0, "L", false, 0, "")
			line := mapString(f, "message")
			if cat := mapString(f, "category"); cat != "" {
				line = cat + ": " + line
			}
			pdf.MultiCell(0, 10, safePDFString(line), "", "L", false)
		}
		pdf.Ln(4)
	}

	candidates := mapSlice(inv, "candidate_improvements")
	var shown int
	for _, c := range candidates {
		sql := strings.TrimSpace(mapString(c, "proposed_change"))
		if sql == "" || !looksLikeSQLRewrite(sql) {
			continue
		}
		if shown == 0 {
			sectionTitle(pdf, "Candidate rewrite")
		}
		shown++
		if shown > 2 {
			break
		}
		pdf.SetFont("Courier", "", 8)
		pdf.SetFillColor(245, 245, 245)
		if len(sql) > 800 {
			sql = sql[:800] + "..."
		}
		pdf.MultiCell(0, 11, safePDFString(sql), "1", "L", true)
		pdf.SetFont("Helvetica", "", 10)
		if why := mapString(c, "why_it_might_help"); why != "" {
			pdf.Ln(2)
			pdf.MultiCell(0, 10, safePDFString(why), "", "L", false)
		}
		pdf.Ln(4)
	}

	if tests := mapAny(inv, "controlled_test_results"); tests != nil {
		metrics := mapSlice(tests, "metrics")
		if len(metrics) > 0 {
			sectionTitle(pdf, "Controlled test")
			for _, m := range metrics {
				line := fmt.Sprintf("%s: %s -> %s (%s)",
					mapString(m, "evidence"), mapString(m, "before"), mapString(m, "after"), mapString(m, "change"))
				pdf.CellFormat(12, 10, "", "", 0, "L", false, 0, "")
				pdf.MultiCell(0, 10, safePDFString(line), "", "L", false)
			}
			pdf.Ln(4)
		}
	}

	if eq := mapAny(inv, "equivalence_validation"); eq != nil {
		sectionTitle(pdf, "Result equivalence")
		status := mapString(eq, "status")
		if status == "" {
			status = "Unverified"
		}
		pdf.MultiCell(0, 10, safePDFString("Status: "+status), "", "L", false)
		if notes := mapString(eq, "notes"); notes != "" {
			pdf.MultiCell(0, 10, safePDFString(notes), "", "L", false)
		}
		pdf.Ln(4)
	}

	if next := mapString(inv, "recommended_next_action"); next != "" {
		sectionTitle(pdf, "Recommended next action")
		pdf.MultiCell(0, 10, safePDFString(next), "", "L", false)
		pdf.Ln(4)
	}

	if report.Narrative != nil && len(report.Narrative.Limitations) > 0 {
		sectionTitle(pdf, "Limitations")
		for _, s := range report.Narrative.Limitations {
			pdf.CellFormat(12, 10, "", "", 0, "L", false, 0, "")
			pdf.MultiCell(0, 10, safePDFString(s), "", "L", false)
		}
	}
}

func collapseInvestigationFindings(findings []map[string]any) []map[string]any {
	if len(findings) == 0 {
		return nil
	}
	type group struct {
		first map[string]any
		n     int
	}
	order := make([]string, 0)
	grouped := make(map[string]*group)
	for _, f := range findings {
		msg := mapString(f, "message")
		key := mapString(f, "category") + "\x00" + queryrunner.FindingFingerprint(msg)
		if g, ok := grouped[key]; ok {
			g.n++
			continue
		}
		order = append(order, key)
		grouped[key] = &group{first: f, n: 1}
	}
	out := make([]map[string]any, 0, len(order))
	for _, key := range order {
		g := grouped[key]
		item := g.first
		if g.n > 1 {
			copied := make(map[string]any, len(item))
			for k, v := range item {
				copied[k] = v
			}
			copied["message"] = queryrunner.FormatCollapsedFinding(mapString(item, "message"), g.n)
			item = copied
		}
		out = append(out, item)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return queryrunner.FindingDisplayRank(mapString(out[i], "category"), mapString(out[i], "message")) <
			queryrunner.FindingDisplayRank(mapString(out[j], "category"), mapString(out[j], "message"))
	})
	return out
}

func looksLikeSQLRewrite(s string) bool {
	head := strings.ToLower(strings.TrimSpace(s))
	return strings.HasPrefix(head, "select") || strings.HasPrefix(head, "with")
}

func sectionTitle(pdf *gofpdf.Fpdf, title string) {
	pdf.SetFont("Helvetica", "B", 10)
	pdf.SetTextColor(80, 80, 80)
	pdf.CellFormat(0, 10, title, "", 1, "L", false, 0, "")
	pdf.SetTextColor(0, 0, 0)
	pdf.SetFont("Helvetica", "", 10)
}

// drawMetricsBarChart draws a simple bar chart from time series metrics (current period values).
func drawMetricsBarChart(pdf *gofpdf.Fpdf, timeSeries map[string]*reports.TimeSeriesData) {
	sectionTitle(pdf, "Metrics overview (chart)")
	measures := make([]string, 0, len(timeSeries))
	var maxVal float64
	for m, ts := range timeSeries {
		if ts == nil {
			continue
		}
		measures = append(measures, m)
		if ts.CurrentPeriod > maxVal {
			maxVal = ts.CurrentPeriod
		}
	}
	if len(measures) == 0 {
		return
	}
	sort.Strings(measures)
	const (
		chartLeft   = 20.0
		labelWidth  = 90.0
		barAreaW    = 280.0
		barHeight   = 14.0
		barGap      = 4.0
		barMaxWidth = barAreaW - 10
	)
	if maxVal <= 0 {
		maxVal = 1
	}
	pdf.SetFont("Helvetica", "", 9)
	for _, measure := range measures {
		ts := timeSeries[measure]
		if ts == nil {
			continue
		}
		v := ts.CurrentPeriod
		barW := (v / maxVal) * barMaxWidth
		if barW < 2 && v > 0 {
			barW = 2
		}
		label := safePDFString(measure)
		if len(label) > 18 {
			label = label[:15] + "..."
		}
		pdf.CellFormat(chartLeft, barHeight, "", "", 0, "L", false, 0, "")
		pdf.CellFormat(labelWidth, barHeight, label, "", 0, "L", false, 0, "")
		pdf.SetFillColor(70, 130, 180) // steel blue
		pdf.Rect(chartLeft+labelWidth, pdf.GetY()+2, barW, barHeight-4, "F")
		pdf.CellFormat(barAreaW-barW, barHeight, "", "", 0, "L", false, 0, "")
		pdf.CellFormat(0, barHeight, fmt.Sprintf("%.2f", v), "", 1, "R", false, 0, "")
		pdf.SetFillColor(255, 255, 255)
	}
}

// safePDFString returns a string safe for gofpdf (Helvetica is ASCII/Latin-1). Non-ASCII runes are replaced with '?'.
func safePDFString(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 128 || (r >= 160 && r <= 255) {
			b.WriteRune(r)
		} else {
			b.WriteRune('?')
		}
	}
	return b.String()
}
