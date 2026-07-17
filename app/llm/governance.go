package llm

import (
	"strings"

	appconfig "github.com/pgquerynarrative/pgquerynarrative/app/config"
)

// DataClass identifies categories of data that may be sent to an LLM provider.
type DataClass string

const (
	DataClassSQL       DataClass = "sql_text"
	DataClassSchema    DataClass = "schema_metadata"
	DataClassMetrics   DataClass = "metrics"
	DataClassRowValues DataClass = "row_values"
	DataClassRAG       DataClass = "rag_context"
)

// PolicyDecision records whether an LLM call was allowed and why.
type PolicyDecision string

const (
	PolicyAllowLocal    PolicyDecision = "allow_local"
	PolicyAllowCloud    PolicyDecision = "allow_cloud"
	PolicyDenyCloud     PolicyDecision = "deny_cloud"
	PolicyDenyRows      PolicyDecision = "deny_row_data"
	PolicyDenyBudget    PolicyDecision = "deny_budget"
	PolicyDenyInjection PolicyDecision = "deny_injection"
)

// GovernanceInput describes an outbound LLM request for policy evaluation.
type GovernanceInput struct {
	Provider    string
	SendRowData bool
	AllowCloud  bool
	RedactPII   bool
	HasRows     bool
	HasRAG      bool
	SQLText     string
}

// GovernanceResult is the outcome of policy evaluation.
type GovernanceResult struct {
	Allowed      bool
	Decision     PolicyDecision
	DataClasses  []DataClass
	ErrorMessage string
}

// EvaluateGovernance applies data-governance rules before an external LLM call.
func EvaluateGovernance(in GovernanceInput) GovernanceResult {
	if strings.TrimSpace(in.SQLText) != "" && ContainsPromptInjection(in.SQLText) {
		return GovernanceResult{
			Allowed:      false,
			Decision:     PolicyDenyInjection,
			DataClasses:  []DataClass{DataClassSQL},
			ErrorMessage: "query text contains disallowed instruction-override patterns",
		}
	}
	classes := []DataClass{DataClassSQL, DataClassSchema, DataClassMetrics}
	if in.HasRAG {
		classes = append(classes, DataClassRAG)
	}
	if in.SendRowData && in.HasRows {
		classes = append(classes, DataClassRowValues)
	}
	cloud := appconfig.IsCloudLLMProvider(in.Provider)
	if cloud && !in.AllowCloud {
		return GovernanceResult{
			Allowed:      false,
			Decision:     PolicyDenyCloud,
			DataClasses:  classes,
			ErrorMessage: "cloud LLM provider requires explicit LLM_ALLOW_EXTERNAL_DATA approval",
		}
	}
	if cloud && in.SendRowData && in.HasRows && !in.RedactPII {
		return GovernanceResult{
			Allowed:      false,
			Decision:     PolicyDenyRows,
			DataClasses:  classes,
			ErrorMessage: "row data to cloud providers requires LLM_REDACT_PII=true",
		}
	}
	decision := PolicyAllowLocal
	if cloud {
		decision = PolicyAllowCloud
	}
	return GovernanceResult{Allowed: true, Decision: decision, DataClasses: classes}
}

// ClassifyPromptOptions returns data classes implied by prompt construction options.
func ClassifyPromptOptions(opts PromptOptions, hasRows bool) []DataClass {
	in := GovernanceInput{
		Provider:    "ollama",
		SendRowData: opts.SendRowData,
		AllowCloud:  true,
		RedactPII:   opts.RedactPII,
		HasRows:     hasRows,
	}
	return EvaluateGovernance(in).DataClasses
}

// FormatDataClasses returns a comma-separated label for audit storage.
func FormatDataClasses(classes []DataClass) []string {
	out := make([]string, 0, len(classes))
	for _, c := range classes {
		out = append(out, string(c))
	}
	return out
}

// EstimateTokenCount provides a rough token estimate for audit metrics.
func EstimateTokenCount(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	return len(text) / 4
}
