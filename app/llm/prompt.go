package llm

import (
	"fmt"
	"strings"

	"github.com/pgquerynarrative/pgquerynarrative/app/format"
	"github.com/pgquerynarrative/pgquerynarrative/app/metrics"
)

// BuildNarrativePrompt creates a prompt for narrative generation from query results.
// hasPeriodComparison should be true only when metrics contain time_series with a real previous period (so the narrative may mention "vs previous period").
// similarQueriesContext is optional RAG context: short descriptions of similar past queries to ground the narrative.
func BuildNarrativePrompt(sql string, columns []string, rows [][]interface{}, metricsJSON string, hasPeriodComparison bool, similarQueriesContext string, opts PromptOptions) string {
	if opts.MaxSampleRows <= 0 && opts.SendRowData {
		opts.MaxSampleRows = DefaultPromptOptions().MaxSampleRows
	}
	var sb strings.Builder

	sb.WriteString("You are a data analyst expert. Your task is to convert SQL query results into a clear, evidence-based business narrative.\n\n")
	sb.WriteString("IMPORTANT RULES:\n")
	sb.WriteString("1. Only make claims that are directly supported by the data provided\n")
	sb.WriteString("2. Cite only numbers that appear in Sample Data or CALCULATED METRICS. Do not swap columns (e.g. trip count vs revenue) or invent comparisons. Preserve exact magnitude; use comma thousands separator (e.g. 84,816,006.54 not 848 million).\n")
	sb.WriteString("3. Format percentages with one decimal place only (e.g. 25.1%, 24.9%). Never output long decimals like 25.059274868647645%.\n")
	sb.WriteString("4. Do not make assumptions or inferences beyond what the data shows\n")
	sb.WriteString("5. Acknowledge limitations if the dataset is small or incomplete\n")
	sb.WriteString("6. Use clear, professional business language\n")
	sb.WriteString("7. Only mention \"previous period\", \"prior period\", \"vs last period\", or \"same period last year\" if CALCULATED METRICS actually contain time_series with current_period and previous_period for that measure. If there is no such comparison in the metrics, do NOT invent one.\n")
	sb.WriteString("8. When stating a rate (e.g. revenue per trip, average fare), use the correct scale: e.g. dollars per trip should be in the tens or low hundreds, not hundreds of thousands. Match the units in the data.\n")
	sb.WriteString("9. When describing totals, use the scale from the data (e.g. if sample shows 1,234,567.89 then \"$1.2 million\" or \"$1,234,567.89\", not \"$1.2 billion\").\n")
	sb.WriteString("10. If CALCULATED METRICS include time_series with period-over-period comparison, include at least one takeaway that mentions how key measures changed vs the previous period, using the numbers from the metrics.\n")
	sb.WriteString("11. When time_series includes forecast_ci_lower and forecast_ci_upper (confidence interval for the next-period forecast), mention the range in a takeaway (e.g. \"expected to be within X–Y\") to convey uncertainty.\n\n")
	if !hasPeriodComparison {
		sb.WriteString("NOTE: This result has no period-over-period comparison in the metrics. Do not mention \"previous period\", \"prior period\", \"same period last year\", or \"compared to last year\".\n\n")
	}

	sb.WriteString("SQL QUERY:\n")
	sb.WriteString(sql)
	sb.WriteString("\n\n")
	if similarQueriesContext != "" {
		sb.WriteString("SIMILAR PAST QUERIES (for context only; do not invent data from these):\n")
		sb.WriteString(similarQueriesContext)
		sb.WriteString("\n\n")
	}
	sb.WriteString("QUERY RESULTS:\n")
	sb.WriteString("Columns: ")
	sb.WriteString(strings.Join(columns, ", "))
	sb.WriteString("\n\n")

	if opts.SendRowData && len(rows) > 0 {
		sampleRows := rows
		if opts.RedactPII {
			sampleRows = RedactRows(columns, sampleRows)
		}
		if len(sampleRows) > opts.MaxSampleRows {
			sampleRows = sampleRows[:opts.MaxSampleRows]
		}

		sb.WriteString(fmt.Sprintf("Sample Data (showing first %d rows):\n", len(sampleRows)))
		maxCols := 0
		for _, row := range sampleRows {
			if len(row) > maxCols {
				maxCols = len(row)
			}
		}
		rowStr := make([]string, maxCols)
		for i, row := range sampleRows {
			sb.WriteString(fmt.Sprintf("Row %d: ", i+1))
			n := len(row)
			for j := 0; j < n; j++ {
				rowStr[j] = formatCellForPrompt(row[j])
			}
			sb.WriteString(strings.Join(rowStr[:n], " | "))
			sb.WriteString("\n")
		}

		if len(rows) > opts.MaxSampleRows {
			sb.WriteString(fmt.Sprintf("... and %d more rows\n", len(rows)-opts.MaxSampleRows))
		}
	}
	if !opts.SendRowData {
		sb.WriteString("Row values omitted by data governance policy (columns and metrics only).\n")
	}

	sb.WriteString("\n")
	sb.WriteString("CALCULATED METRICS (raw JSON; when citing numbers in your narrative, format with comma thousands separator and preserve exact magnitude, e.g. 84816006.54 -> 84,816,006.54):\n")
	sb.WriteString(metricsJSON)
	sb.WriteString("\n\n")
	if !hasPeriodComparison {
		sb.WriteString("REMINDER: There is no period-over-period comparison in the metrics above (no time_series with previous_period). Your headline and takeaways must NOT mention \"previous period\", \"prior period\", \"compared to last period\", or \"same period last year\". Describe only what the single result shows.\n\n")
	}

	sb.WriteString("TASK:\n")
	sb.WriteString("Generate a business narrative in JSON format with the following structure:\n")
	sb.WriteString(`{
  "headline": "A concise one-sentence summary of the key finding",
  "takeaways": [
    "Key insight 1 with specific numbers",
    "Key insight 2 with specific numbers",
    "Key insight 3 with specific numbers"
  ],
  "drivers": [
    "Factor that explains the results (if applicable)"
  ],
  "limitations": [
    "Any limitations or caveats about the data or analysis"
  ],
  "recommendations": [
    "Actionable recommendation based on the data (optional)"
  ]
}`)
	sb.WriteString("\n\n")
	sb.WriteString("Return ONLY valid JSON. Do not include any markdown formatting, code blocks, or explanatory text outside the JSON.\n")

	return sb.String()
}

// BuildNarrativeRewritePrompt asks the LLM to rewrite an existing narrative while
// preserving the same JSON structure used by reports.
func BuildNarrativeRewritePrompt(instruction, narrativeJSON, metricsJSON string) string {
	var sb strings.Builder
	sb.WriteString("You are a business writing editor for analytics narratives.\n")
	sb.WriteString("Rewrite the narrative JSON according to this instruction:\n")
	sb.WriteString(instruction)
	sb.WriteString("\n\n")
	sb.WriteString("Rules:\n")
	sb.WriteString("1. Return STRICT valid JSON only.\n")
	sb.WriteString("2. Keep exactly these keys: headline, takeaways, drivers, limitations, recommendations.\n")
	sb.WriteString("3. Keep claims grounded in provided narrative and metrics context.\n")
	sb.WriteString("4. Do not invent new metric values.\n")
	sb.WriteString("5. Keep arrays as arrays of short strings.\n\n")
	sb.WriteString("Current narrative JSON:\n")
	sb.WriteString(narrativeJSON)
	sb.WriteString("\n\n")
	sb.WriteString("Metrics context JSON (for grounding only):\n")
	sb.WriteString(metricsJSON)
	sb.WriteString("\n\n")
	sb.WriteString("Return only JSON with the same schema.")
	return sb.String()
}

// formatCellForPrompt formats a cell value for the LLM prompt so numbers use comma-separated thousands,
// reducing the chance the model misreads scale (e.g. 84816006.54 -> "84,816,006.54").
func formatCellForPrompt(val interface{}) string {
	if val == nil {
		return "NULL"
	}
	f, ok := metrics.GetNumericValue(val)
	if ok {
		return formatFloatWithCommas(f)
	}
	return fmt.Sprint(val)
}

var formatFloatWithCommas = format.FloatWithCommas
