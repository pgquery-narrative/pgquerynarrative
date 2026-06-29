package llm_test

import (
	"strings"
	"testing"

	"github.com/pgquerynarrative/pgquerynarrative/app/llm"
)

func TestBuildNarrativePrompt_ContainsSQLAndColumns(t *testing.T) {
	sql := "SELECT region, SUM(total_amount) FROM demo.sales GROUP BY region"
	columns := []string{"region", "total"}
	rows := [][]interface{}{{"North", 1000.0}, {"South", 500.0}}
	metricsJSON := `{"aggregates":{"total":{"sum":1500}}}`

	prompt := llm.BuildNarrativePrompt(sql, columns, rows, metricsJSON, false, "", llm.DefaultPromptOptions())

	if !strings.Contains(prompt, sql) {
		t.Error("prompt should contain the SQL query")
	}
	if !strings.Contains(prompt, "region") || !strings.Contains(prompt, "total") {
		t.Error("prompt should contain column names")
	}
	if !strings.Contains(prompt, metricsJSON) {
		t.Error("prompt should contain metrics JSON")
	}
	if !strings.Contains(prompt, "Sample Data") {
		t.Error("prompt should include sample data section")
	}
}

func TestBuildNarrativePrompt_NoPeriodComparisonIncludesReminder(t *testing.T) {
	prompt := llm.BuildNarrativePrompt("SELECT 1", []string{"x"}, nil, "{}", false, "", llm.DefaultPromptOptions())

	if !strings.Contains(prompt, "no period-over-period") {
		t.Error("when hasPeriodComparison is false, prompt should include no period-over-period reminder")
	}
	if !strings.Contains(prompt, "Do not mention") {
		t.Error("prompt should instruct not to mention previous period")
	}
}

func TestBuildNarrativePrompt_PeriodComparisonOmitsReminder(t *testing.T) {
	prompt := llm.BuildNarrativePrompt("SELECT 1", []string{"x"}, nil, "{}", true, "", llm.DefaultPromptOptions())

	if strings.Contains(prompt, "no period-over-period comparison in the metrics") {
		t.Error("when hasPeriodComparison is true, prompt should not include the no-comparison reminder")
	}
}

func TestBuildNarrativePrompt_TruncatesRowsToTen(t *testing.T) {
	columns := []string{"a"}
	rows := make([][]interface{}, 15)
	for i := range rows {
		rows[i] = []interface{}{i}
	}

	prompt := llm.BuildNarrativePrompt("SELECT a FROM t", columns, rows, "{}", false, "", llm.PromptOptions{MaxSampleRows: 10, SendRowData: true})

	if !strings.Contains(prompt, "Row 10:") {
		t.Error("prompt should include at least 10 rows")
	}
	if !strings.Contains(prompt, "... and 5 more rows") {
		t.Error("prompt should mention remaining rows when more than 10")
	}
}

func TestBuildNarrativePrompt_OmitsRowsWhenDisabled(t *testing.T) {
	prompt := llm.BuildNarrativePrompt("SELECT a FROM demo.sales", []string{"a"}, [][]interface{}{{1}}, "{}", false, "", llm.PromptOptions{SendRowData: false})
	if strings.Contains(prompt, "Row 1:") {
		t.Fatal("row values should be omitted when SendRowData is false")
	}
	if !strings.Contains(prompt, "Row values omitted") {
		t.Fatal("prompt should note row omission policy")
	}
}
