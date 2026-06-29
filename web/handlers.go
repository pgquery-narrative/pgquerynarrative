package web

import (
	"context"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/pgquerynarrative/pgquerynarrative/api/gen/queries"
	"github.com/pgquerynarrative/pgquerynarrative/api/gen/reports"
	"github.com/pgquerynarrative/pgquerynarrative/app/format"
)

type Handlers struct {
	reportsEndpoints *reports.Endpoints
}

func NewHandlers(queriesEndpoints *queries.Endpoints, reportsEndpoints *reports.Endpoints) *Handlers {
	return &Handlers{reportsEndpoints: reportsEndpoints}
}

var formatFloatWithCommas = format.FloatWithCommas

// ExportReport serves a self-contained HTML file for the report (download/export).
// Query param: id (report UUID). Response is attachment with filename report-<id>.html.
func (h *Handlers) ExportReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	reportID := r.URL.Query().Get("id")
	if reportID == "" {
		http.Error(w, "missing report id", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	payload := &reports.GetPayload{ID: reportID}
	got, err := h.reportsEndpoints.Get(ctx, payload)
	if err != nil {
		if _, ok := err.(*reports.NotFoundError); ok {
			http.Error(w, "report not found", http.StatusNotFound)
			return
		}
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	report, ok := got.(*reports.Report)
	if !ok {
		http.Error(w, "invalid report", http.StatusInternalServerError)
		return
	}

	// Build full HTML document with inline CSS so the file is self-contained and print-friendly
	bodyHTML := FormatReportHTML(report)
	// Short id for filename (first 8 chars of UUID)
	filenameID := reportID
	if len(filenameID) > 8 {
		filenameID = filenameID[:8]
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\"report-"+filenameID+".html\"")
	_, _ = io.WriteString(w, buildExportHTML(report.SQL, report.CreatedAt, bodyHTML))
}

// ExportReportPDF serves the report as a PDF file (download).
// Query param: id (report UUID). Response is attachment with filename report-<id>.pdf.
func (h *Handlers) ExportReportPDF(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	reportID := r.URL.Query().Get("id")
	if reportID == "" {
		http.Error(w, "missing report id", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	payload := &reports.GetPayload{ID: reportID}
	got, err := h.reportsEndpoints.Get(ctx, payload)
	if err != nil {
		if _, ok := err.(*reports.NotFoundError); ok {
			http.Error(w, "report not found", http.StatusNotFound)
			return
		}
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	report, ok := got.(*reports.Report)
	if !ok {
		http.Error(w, "invalid report", http.StatusInternalServerError)
		return
	}

	filenameID := reportID
	if len(filenameID) > 8 {
		filenameID = filenameID[:8]
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", "attachment; filename=\"report-"+filenameID+".pdf\"")

	if err := BuildReportPDF(w, report); err != nil {
		http.Error(w, "failed to generate PDF", http.StatusInternalServerError)
		return
	}
}

// ExportSharedReportPDF serves a shared report by token as a PDF file.
func (h *Handlers) ExportSharedReportPDF(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token := r.URL.Query().Get("token")
	if token == "" {
		http.Error(w, "missing share token", http.StatusBadRequest)
		return
	}
	report, err := h.getSharedReport(r.Context(), token)
	if err != nil {
		if _, ok := err.(*reports.NotFoundError); ok {
			http.Error(w, "shared report not found or expired", http.StatusNotFound)
			return
		}
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	filenameID := report.ID
	if len(filenameID) > 8 {
		filenameID = filenameID[:8]
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", "attachment; filename=\"shared-report-"+filenameID+".pdf\"")
	if err := BuildReportPDF(w, report); err != nil {
		http.Error(w, "failed to generate PDF", http.StatusInternalServerError)
		return
	}
}

func (h *Handlers) getSharedReport(ctx context.Context, token string) (*reports.Report, error) {
	got, err := h.reportsEndpoints.GetShared(ctx, &reports.GetSharedPayload{Token: token})
	if err != nil {
		return nil, err
	}
	report, ok := got.(*reports.Report)
	if !ok {
		return nil, fmt.Errorf("invalid shared report payload")
	}
	return report, nil
}

// buildExportHTML returns a complete HTML document with embedded styles for the report body.
func buildExportHTML(sql, createdAt, bodyHTML string) string {
	created := createdAt
	if created == "" {
		created = "—"
	}
	// Truncate SQL for display; template will escape when rendering.
	sqlDisplay := sql
	if r := []rune(sql); len(r) > 500 {
		sqlDisplay = string(r[:500]) + "…"
	}
	tmpl := template.Must(template.New("export").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Report — PgQueryNarrative</title>
<style>
{{.Styles}}
</style>
</head>
<body class="export-body">
<header class="export-header">
  <h1>PgQueryNarrative Report</h1>
  <p class="export-meta">Generated: {{.Created}}</p>
  <details class="export-sql"><summary>Query</summary><pre>{{.SQL}}</pre></details>
</header>
<main class="export-main">
{{.Body}}
</main>
<footer class="export-footer">Exported from PgQueryNarrative. Open in a browser or print to PDF.</footer>
</body>
</html>`))
	data := struct {
		Styles  string
		Created string
		SQL     string
		Body    template.HTML
	}{
		Styles:  reportExportStyles(),
		Created: created,
		SQL:     sqlDisplay,
		// #nosec G203 -- bodyHTML is built only from template.HTMLEscapeString in FormatReportHTML
		Body: template.HTML(bodyHTML),
	}
	var buf strings.Builder
	if err := tmpl.Execute(&buf, data); err != nil {
		return "" // fallback; caller should handle
	}
	return buf.String()
}

// reportExportStyles returns inline CSS for the exported report (self-contained, print-friendly).
func reportExportStyles() string {
	return `
body.export-body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; line-height: 1.6; color: #0f172a; background: #f8fafc; margin: 0; padding: 24px; font-size: 15px; }
.export-header { margin-bottom: 24px; padding-bottom: 16px; border-bottom: 1px solid #e2e8f0; }
.export-header h1 { font-size: 1.5rem; margin: 0 0 8px 0; font-weight: 600; }
.export-meta { color: #64748b; font-size: 0.875rem; margin: 0; }
.export-sql { margin-top: 12px; font-size: 0.8125rem; }
.export-sql summary { cursor: pointer; color: #0ea5e9; font-weight: 500; }
.export-sql pre { background: #f1f5f9; padding: 12px; border-radius: 6px; overflow-x: auto; margin: 8px 0 0 0; white-space: pre-wrap; word-break: break-word; }
.export-main { max-width: 800px; }
.export-footer { margin-top: 32px; padding-top: 16px; border-top: 1px solid #e2e8f0; color: #94a3b8; font-size: 0.8125rem; }
.report-results { margin: 0; background: #fff; border-radius: 10px; box-shadow: 0 1px 3px rgba(0,0,0,0.06); border: 1px solid #e2e8f0; overflow: hidden; }
.report-meta { display: flex; flex-wrap: wrap; align-items: center; gap: 14px; padding: 14px 20px; background: #f1f5f9; border-bottom: 1px solid #e2e8f0; font-size: 0.8125rem; color: #64748b; }
.report-id { font-family: ui-monospace, monospace; color: #0f172a; }
.report-model { display: inline-block; padding: 4px 10px; background: #e0e7ff; color: #3730a3; border-radius: 6px; font-weight: 500; font-size: 0.8125rem; }
.report-narrative { margin: 0; padding: 24px; background: #fff; }
.report-headline { color: #0f172a; font-size: 1.25rem; line-height: 1.4; margin: 0 0 16px 0; font-weight: 600; }
.report-section-title { color: #64748b; font-size: 0.8125rem; font-weight: 600; margin: 20px 0 8px 0; text-transform: uppercase; letter-spacing: 0.04em; }
.report-section-title:first-of-type { margin-top: 0; }
.report-takeaways, .report-list { margin: 6px 0 0 0; padding-left: 20px; }
.report-takeaways li, .report-list li { margin: 8px 0; line-height: 1.55; color: #0f172a; }
.chart-suggestions, .period-comparison, .correlations, .cohorts, .perf-suggestions, .data-quality { margin-top: 16px; padding: 16px 18px; background: #f1f5f9; border-radius: 6px; border: 1px solid #e2e8f0; }
.report-hint { font-size: 0.8125rem; color: #64748b; margin: 0 0 8px 0; }
.correlation-table, .cohort-period-table { width: 100%; border-collapse: collapse; font-size: 0.875rem; }
.correlation-table th, .correlation-table td, .cohort-period-table th, .cohort-period-table td { padding: 6px 10px; text-align: left; border-bottom: 1px solid #e2e8f0; }
.correlation-table th, .cohort-period-table th { font-weight: 600; color: #64748b; }
.cohort-block { margin-top: 12px; }
.cohort-retention { margin: 4px 0 8px 0; font-size: 0.875rem; color: #64748b; }
.period-comparison-hint { font-size: 0.8125rem; color: #64748b; margin: 0 0 8px 0; }
.period-labels { font-weight: normal; }
.period-comparison-list { margin: 8px 0 0 0; padding-left: 20px; }
.period-item { margin: 6px 0; }
.trend-increasing { color: #059669; }
.trend-decreasing { color: #dc2626; }
.trend-stable { color: #64748b; }
.trend-badge.trend-increasing { background: #d1fae5; color: #059669; }
.trend-badge.trend-decreasing { background: #fee2e2; color: #dc2626; }
.trend-badge.trend-stable { background: #f1f5f9; color: #64748b; }
.advanced-metrics { margin-top: 16px; }
.advanced-metric-measure { margin-bottom: 16px; }
.report-measure-title { font-size: 0.9375rem; font-weight: 600; color: #0f172a; margin-bottom: 8px; }
.trend-summary { margin: 0 0 10px 0; font-size: 0.9375rem; line-height: 1.5; }
.next-period-forecast { margin: 6px 0 10px 0; font-size: 0.9375rem; }
.anomalies-list { margin: 8px 0; font-size: 0.875rem; }
.anomaly-list { margin: 4px 0 0 16px; padding-left: 0; list-style: disc; }
.anomaly-period { font-weight: 500; color: #0f172a; }
.period-history-details { margin-top: 8px; font-size: 0.875rem; }
.period-history-details summary { cursor: pointer; font-weight: 500; color: #0ea5e9; }
.period-history-table { width: 100%; margin-top: 8px; border-collapse: collapse; font-size: 0.8125rem; }
.period-history-table th, .period-history-table td { padding: 6px 10px; text-align: left; border-bottom: 1px solid #e2e8f0; }
.period-history-table th { font-weight: 600; color: #64748b; }
.data-quality-table { width: 100%; border-collapse: collapse; font-size: 0.875rem; }
.data-quality-table th, .data-quality-table td { text-align: left; padding: 6px 12px 6px 0; border-bottom: 1px solid #e2e8f0; }
.data-quality-table th { color: #64748b; font-weight: 600; }
.suggestion-list { list-style: none; padding-left: 0; }
.suggestion-list li { margin: 0; }
@media print { .export-body { background: #fff; padding: 0; } .export-header, .export-footer { border-color: #e2e8f0; } }
`
}

// FormatReportHTML formats report results as HTML with model badge and improved layout.
func FormatReportHTML(report *reports.Report) string {
	var sb strings.Builder
	sb.Grow(2048)
	sb.WriteString("<div class=\"report-results\">")
	// Report id and model on one line
	sb.WriteString("<div class=\"report-meta\">")
	sb.WriteString("<span class=\"report-id\">Report: ")
	sb.WriteString(template.HTMLEscapeString(report.ID))
	sb.WriteString("</span>")
	if report.LlmProvider != "" || report.LlmModel != "" {
		modelLabel := report.LlmProvider
		if report.LlmModel != "" {
			if modelLabel != "" {
				modelLabel += " / "
			}
			modelLabel += report.LlmModel
		}
		sb.WriteString("<span class=\"report-model\">Model: ")
		sb.WriteString(template.HTMLEscapeString(modelLabel))
		sb.WriteString("</span>")
	}
	sb.WriteString("</div>")

	if len(report.ChartSuggestions) > 0 {
		sb.WriteString("<div class=\"chart-suggestions\"><h4 class=\"report-section-title\">Suggested charts</h4><ul class=\"suggestion-list report-list\">")
		for _, s := range report.ChartSuggestions {
			if s == nil {
				continue
			}
			sb.WriteString("<li><strong>")
			sb.WriteString(template.HTMLEscapeString(s.Label))
			sb.WriteString("</strong> — ")
			sb.WriteString(template.HTMLEscapeString(s.Reason))
			sb.WriteString("</li>")
		}
		sb.WriteString("</ul></div>")
	}

	if report.Metrics != nil && len(report.Metrics.TimeSeries) > 0 {
		sb.WriteString("<div class=\"period-comparison\" title=\"Compares the latest period in the result to the period before it.\"><h4 class=\"report-section-title\">Vs previous period")
		if report.Metrics.PeriodCurrentLabel != nil && report.Metrics.PeriodPreviousLabel != nil && *report.Metrics.PeriodCurrentLabel != "" && *report.Metrics.PeriodPreviousLabel != "" {
			sb.WriteString(" <span class=\"period-labels\">(")
			sb.WriteString(template.HTMLEscapeString(*report.Metrics.PeriodCurrentLabel))
			sb.WriteString(" vs ")
			sb.WriteString(template.HTMLEscapeString(*report.Metrics.PeriodPreviousLabel))
			sb.WriteString(")</span>")
		}
		sb.WriteString("</h4><p class=\"period-comparison-hint\">Latest period vs the one before it.</p><ul class=\"period-comparison-list\">")
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
			sb.WriteString("<li class=\"period-item\"><strong>")
			sb.WriteString(template.HTMLEscapeString(measure))
			sb.WriteString("</strong>: ")
			sb.WriteString(formatFloatWithCommas(ts.CurrentPeriod))
			if ts.PreviousPeriod != nil {
				sb.WriteString(" (prev: ")
				sb.WriteString(formatFloatWithCommas(*ts.PreviousPeriod))
				sb.WriteString(")")
			}
			if ts.ChangePercentage != nil {
				sb.WriteString(" <span class=\"period-change trend-")
				sb.WriteString(template.HTMLEscapeString(ts.Trend))
				sb.WriteString("\">")
				if *ts.ChangePercentage >= 0 {
					sb.WriteString("+")
				}
				sb.WriteString(template.HTMLEscapeString(fmt.Sprintf("%.1f%%", *ts.ChangePercentage)))
				sb.WriteString("</span>")
			}
			sb.WriteString("</li>")
		}
		sb.WriteString("</ul></div>")

		// Advanced metrics: trend summary, anomalies, period history per measure
		sb.WriteString("<div class=\"advanced-metrics\">")
		for _, measure := range measures {
			ts := report.Metrics.TimeSeries[measure]
			if ts == nil {
				continue
			}
			hasAdvanced := (ts.TrendSummary != nil && ts.TrendSummary.Summary != "") ||
				len(ts.Anomalies) > 0 || len(ts.Periods) > 0
			if !hasAdvanced {
				continue
			}
			sb.WriteString("<div class=\"advanced-metric-measure\"><h5 class=\"report-measure-title\">")
			sb.WriteString(template.HTMLEscapeString(measure))
			sb.WriteString("</h5>")

			if ts.TrendSummary != nil && ts.TrendSummary.Summary != "" {
				sb.WriteString("<p class=\"trend-summary\"><span class=\"trend-badge trend-")
				sb.WriteString(template.HTMLEscapeString(ts.TrendSummary.Direction))
				sb.WriteString("\">")
				sb.WriteString(template.HTMLEscapeString(ts.TrendSummary.Direction))
				sb.WriteString("</span> ")
				sb.WriteString(template.HTMLEscapeString(ts.TrendSummary.Summary))
				if ts.MovingAverage != nil {
					sb.WriteString(" Moving avg (latest): ")
					sb.WriteString(formatFloatWithCommas(*ts.MovingAverage))
					sb.WriteString(".")
				}
				sb.WriteString("</p>")
			}
			if ts.NextPeriodForecast != nil {
				sb.WriteString("<p class=\"next-period-forecast\"><strong>Next period forecast:</strong> ")
				sb.WriteString(formatFloatWithCommas(*ts.NextPeriodForecast))
				if ts.ForecastCiLower != nil && ts.ForecastCiUpper != nil {
					sb.WriteString(" <span class=\"forecast-ci\">(forecast interval: ")
					sb.WriteString(formatFloatWithCommas(*ts.ForecastCiLower))
					sb.WriteString("–")
					sb.WriteString(formatFloatWithCommas(*ts.ForecastCiUpper))
					sb.WriteString(")</span>")
				}
				sb.WriteString("</p>")
			}

			if len(ts.Anomalies) > 0 {
				sb.WriteString("<div class=\"anomalies-list\"><strong>Anomalies:</strong> <ul class=\"anomaly-list\">")
				for _, a := range ts.Anomalies {
					if a == nil {
						continue
					}
					sb.WriteString("<li><span class=\"anomaly-period\">")
					sb.WriteString(template.HTMLEscapeString(a.PeriodLabel))
					sb.WriteString("</span> ")
					sb.WriteString(formatFloatWithCommas(a.Value))
					sb.WriteString(" — ")
					sb.WriteString(template.HTMLEscapeString(a.Reason))
					sb.WriteString("</li>")
				}
				sb.WriteString("</ul></div>")
			}

			if len(ts.Periods) > 0 {
				sb.WriteString("<details class=\"period-history-details\"><summary>Period history (")
				sb.WriteString(strconv.Itoa(len(ts.Periods)))
				sb.WriteString(" periods)</summary><table class=\"period-history-table\"><thead><tr><th>Period</th><th>Value</th></tr></thead><tbody>")
				// Show last 15 to keep UI compact
				start := 0
				if len(ts.Periods) > 15 {
					start = len(ts.Periods) - 15
				}
				for i := start; i < len(ts.Periods); i++ {
					p := ts.Periods[i]
					if p == nil {
						continue
					}
					sb.WriteString("<tr><td>")
					sb.WriteString(template.HTMLEscapeString(p.Label))
					sb.WriteString("</td><td>")
					sb.WriteString(formatFloatWithCommas(p.Value))
					sb.WriteString("</td></tr>")
				}
				sb.WriteString("</tbody></table></details>")
			}
			sb.WriteString("</div>")
		}
		sb.WriteString("</div>")
	}

	if report.Metrics != nil && len(report.Metrics.Correlations) > 0 {
		sb.WriteString("<div class=\"correlations\"><h4 class=\"report-section-title\">Correlations</h4><p class=\"report-hint\">Pearson and Spearman between numeric measures.</p><table class=\"correlation-table\"><thead><tr><th>Column A</th><th>Column B</th><th>Pearson</th><th>Spearman</th></tr></thead><tbody>")
		for _, c := range report.Metrics.Correlations {
			if c == nil {
				continue
			}
			sb.WriteString("<tr><td>")
			sb.WriteString(template.HTMLEscapeString(c.ColumnA))
			sb.WriteString("</td><td>")
			sb.WriteString(template.HTMLEscapeString(c.ColumnB))
			sb.WriteString("</td><td>")
			sb.WriteString(fmt.Sprintf("%.3f", c.Pearson))
			sb.WriteString("</td><td>")
			sb.WriteString(fmt.Sprintf("%.3f", c.Spearman))
			sb.WriteString("</td></tr>")
		}
		sb.WriteString("</tbody></table></div>")
	}

	if report.Metrics != nil && len(report.Metrics.Cohorts) > 0 {
		sb.WriteString("<div class=\"cohorts\"><h4 class=\"report-section-title\">Cohorts</h4><p class=\"report-hint\">Measure by cohort and period.</p>")
		for _, c := range report.Metrics.Cohorts {
			if c == nil {
				continue
			}
			sb.WriteString("<div class=\"cohort-block\"><h5 class=\"report-measure-title\">")
			sb.WriteString(template.HTMLEscapeString(c.CohortLabel))
			sb.WriteString("</h5>")
			if c.RetentionPct != nil {
				sb.WriteString("<p class=\"cohort-retention\">Retention: ")
				sb.WriteString(template.HTMLEscapeString(fmt.Sprintf("%.1f", *c.RetentionPct)))
				sb.WriteString("%</p>")
			}
			if len(c.Periods) > 0 {
				sb.WriteString("<table class=\"cohort-period-table\"><thead><tr><th>Period</th><th>Value</th></tr></thead><tbody>")
				for _, p := range c.Periods {
					if p == nil {
						continue
					}
					sb.WriteString("<tr><td>")
					sb.WriteString(template.HTMLEscapeString(p.PeriodLabel))
					sb.WriteString("</td><td>")
					sb.WriteString(formatFloatWithCommas(p.Value))
					sb.WriteString("</td></tr>")
				}
				sb.WriteString("</tbody></table>")
			}
			sb.WriteString("</div>")
		}
		sb.WriteString("</div>")
	}

	if report.Metrics != nil && len(report.Metrics.PerfSuggestions) > 0 {
		sb.WriteString("<div class=\"perf-suggestions\"><h4 class=\"report-section-title\">Performance suggestions</h4><ul class=\"report-list\">")
		for _, s := range report.Metrics.PerfSuggestions {
			sb.WriteString("<li>")
			sb.WriteString(template.HTMLEscapeString(s))
			sb.WriteString("</li>")
		}
		sb.WriteString("</ul></div>")
	}

	if report.Metrics != nil && len(report.Metrics.DataQuality) > 0 {
		sb.WriteString("<div class=\"data-quality\"><h4 class=\"report-section-title\">Data quality</h4><table class=\"data-quality-table\"><thead><tr><th>Column</th><th>Nulls</th><th>Null %</th><th>Distinct</th><th>Rows</th></tr></thead><tbody>")
		dqCols := make([]string, 0, len(report.Metrics.DataQuality))
		for c := range report.Metrics.DataQuality {
			dqCols = append(dqCols, c)
		}
		sort.Strings(dqCols)
		for _, col := range dqCols {
			q := report.Metrics.DataQuality[col]
			if q == nil {
				continue
			}
			sb.WriteString("<tr><td>")
			sb.WriteString(template.HTMLEscapeString(col))
			sb.WriteString("</td><td>")
			sb.WriteString(strconv.Itoa(int(q.NullCount)))
			sb.WriteString("</td><td>")
			sb.WriteString(template.HTMLEscapeString(fmt.Sprintf("%.1f", q.NullPct)))
			sb.WriteString("%</td><td>")
			sb.WriteString(strconv.Itoa(int(q.DistinctCount)))
			sb.WriteString("</td><td>")
			sb.WriteString(strconv.Itoa(int(q.TotalRows)))
			sb.WriteString("</td></tr>")
		}
		sb.WriteString("</tbody></table></div>")
	}

	if report.Narrative != nil {
		sb.WriteString("<div class=\"report-narrative narrative-content\">")
		if report.Narrative.Headline != "" {
			sb.WriteString("<h3 class=\"report-headline\">")
			sb.WriteString(template.HTMLEscapeString(report.Narrative.Headline))
			sb.WriteString("</h3>")
		}
		if len(report.Narrative.Takeaways) > 0 {
			sb.WriteString("<h4 class=\"report-section-title\">Key takeaways</h4><ul class=\"report-takeaways\">")
			for _, takeaway := range report.Narrative.Takeaways {
				sb.WriteString("<li>")
				sb.WriteString(template.HTMLEscapeString(takeaway))
				sb.WriteString("</li>")
			}
			sb.WriteString("</ul>")
		}
		if len(report.Narrative.Drivers) > 0 {
			sb.WriteString("<h4 class=\"report-section-title\">Drivers</h4><ul class=\"report-list\">")
			for _, d := range report.Narrative.Drivers {
				sb.WriteString("<li>")
				sb.WriteString(template.HTMLEscapeString(d))
				sb.WriteString("</li>")
			}
			sb.WriteString("</ul>")
		}
		if len(report.Narrative.Limitations) > 0 {
			sb.WriteString("<h4 class=\"report-section-title\">Limitations</h4><ul class=\"report-list\">")
			for _, l := range report.Narrative.Limitations {
				sb.WriteString("<li>")
				sb.WriteString(template.HTMLEscapeString(l))
				sb.WriteString("</li>")
			}
			sb.WriteString("</ul>")
		}
		if len(report.Narrative.Recommendations) > 0 {
			sb.WriteString("<h4 class=\"report-section-title\">Recommendations</h4><ul class=\"report-list\">")
			for _, r := range report.Narrative.Recommendations {
				sb.WriteString("<li>")
				sb.WriteString(template.HTMLEscapeString(r))
				sb.WriteString("</li>")
			}
			sb.WriteString("</ul>")
		}
		sb.WriteString("</div>")
	}

	sb.WriteString("</div>")
	return sb.String()
}
