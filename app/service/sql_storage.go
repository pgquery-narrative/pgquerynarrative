package service

import (
	"encoding/json"
	"strings"

	"github.com/pgquerynarrative/pgquerynarrative/app/llm"
)

// SQLStorageClass classifies how SQL text is persisted at rest.
type SQLStorageClass string

const (
	// SQLClassRaw stores unredacted SQL (not used for EXPLAIN snapshots).
	SQLClassRaw SQLStorageClass = "raw"
	// SQLClassRedacted stores AST/regex-redacted SQL (used when no encryption key is configured).
	SQLClassRedacted SQLStorageClass = "redacted"
	// SQLClassFingerprint stores only a hash/fingerprint without recoverable SQL text.
	SQLClassFingerprint SQLStorageClass = "fingerprint"
	// SQLClassEncrypted stores AES-GCM sealed redacted SQL/plan (preferred when
	// SECURITY_DATA_ENCRYPTION_KEY is set).
	SQLClassEncrypted SQLStorageClass = "encrypted"
)

// redactExplainPlanJSON walks an EXPLAIN plan tree and redacts string literals in
// Filter/Index Cond style fields so predicate values are not retained at rest.
func redactExplainPlanJSON(plan interface{}) json.RawMessage {
	if plan == nil {
		return nil
	}
	redacted := redactPlanValue(plan)
	b, err := json.Marshal(redacted)
	if err != nil {
		// Fall back to string-level redaction of the original serialization.
		raw, mErr := json.Marshal(plan)
		if mErr != nil {
			return nil
		}
		return json.RawMessage(llm.RedactSQL(string(raw)))
	}
	return b
}

func redactPlanValue(v interface{}) interface{} {
	switch t := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(t))
		for k, val := range t {
			out[k] = redactPlanValue(val)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(t))
		for i, val := range t {
			out[i] = redactPlanValue(val)
		}
		return out
	case string:
		return redactPlanString(t)
	default:
		return v
	}
}

func redactPlanString(s string) string {
	if s == "" {
		return s
	}
	// EXPLAIN Filter / Index Cond lines commonly embed literals; reuse SQL redaction.
	if strings.Contains(s, "'") || strings.ContainsAny(s, "0123456789") {
		return llm.RedactSQL(s)
	}
	return s
}
