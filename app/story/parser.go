package story

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const (
	maxHeadlineLen    = 500
	maxBulletLen      = 1000
	maxBullets        = 20
	maxNarrativeBytes = 50_000
)

var (
	htmlTagPattern       = regexp.MustCompile(`(?i)<\s*/?\s*[a-z][a-z0-9]*[^>]*>`)
	scriptURIPattern     = regexp.MustCompile(`(?i)(javascript|data|vbscript)\s*:`)
	eventHandlerPattern  = regexp.MustCompile(`(?i)\bon[a-z]+\s*=`)
	numberInTextPattern  = regexp.MustCompile(`-?\d[\d,]*\.?\d*`)
	numberCleanupPattern = regexp.MustCompile(`[,\s]`)
)

// ParseNarrative extracts JSON from an LLM response, validates it against a
// stricter schema (field presence, length limits, no embedded markup), and
// grounds numerical claims against the metrics that were actually computed
// for the report. metricsJSON is the same JSON passed to the prompt (or the
// stored metrics for rewrite flows); pass "" to skip grounding (e.g. when
// metrics are unavailable). Returns an error when the response cannot be
// trusted as-is so callers fall back to a deterministic narrative.
func ParseNarrative(response string, metricsJSON string) (*NarrativeContent, error) {
	if len(response) > maxNarrativeBytes {
		return nil, fmt.Errorf("narrative response too large (%d bytes)", len(response))
	}
	jsonStr := extractJSON(response)

	var narrative NarrativeContent
	if err := json.Unmarshal([]byte(jsonStr), &narrative); err != nil {
		return nil, fmt.Errorf("failed to parse narrative JSON: %w", err)
	}

	if err := validateSchema(&narrative); err != nil {
		return nil, err
	}
	if err := rejectMarkup(&narrative); err != nil {
		return nil, err
	}
	if err := rejectEmbeddedSQL(&narrative); err != nil {
		return nil, err
	}

	groundNumericalClaims(&narrative, metricsJSON)

	if len(narrative.Takeaways) == 0 {
		return nil, fmt.Errorf("narrative has no takeaways left after grounding validation")
	}

	return &narrative, nil
}

// validateSchema enforces required fields, array sizes, and per-field length
// limits beyond what json.Unmarshal alone guarantees.
func validateSchema(n *NarrativeContent) error {
	n.Headline = strings.TrimSpace(n.Headline)
	if n.Headline == "" {
		return fmt.Errorf("narrative missing required field: headline")
	}
	if len(n.Headline) > maxHeadlineLen {
		return fmt.Errorf("narrative headline exceeds %d characters", maxHeadlineLen)
	}
	if len(n.Takeaways) == 0 {
		return fmt.Errorf("narrative missing required field: takeaways")
	}
	var err error
	if n.Takeaways, err = sanitizeBulletList(n.Takeaways, "takeaways"); err != nil {
		return err
	}
	if n.Drivers, err = sanitizeBulletList(n.Drivers, "drivers"); err != nil {
		return err
	}
	if n.Limitations, err = sanitizeBulletList(n.Limitations, "limitations"); err != nil {
		return err
	}
	if n.Recommendations, err = sanitizeBulletList(n.Recommendations, "recommendations"); err != nil {
		return err
	}
	if len(n.Takeaways) == 0 {
		return fmt.Errorf("narrative missing required field: takeaways")
	}
	return nil
}

func sanitizeBulletList(items []string, field string) ([]string, error) {
	if len(items) > maxBullets {
		return nil, fmt.Errorf("narrative field %q exceeds %d entries", field, maxBullets)
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if len(item) > maxBulletLen {
			return nil, fmt.Errorf("narrative field %q entry exceeds %d characters", field, maxBulletLen)
		}
		out = append(out, item)
	}
	return out, nil
}

// rejectMarkup returns an error when any narrative field contains HTML tags,
// script/data/vbscript URIs, or inline event handler attributes. LLM output
// is rendered as plain text in the UI; embedded markup could otherwise be
// used for stored XSS if a caller ever renders it unescaped.
func rejectMarkup(n *NarrativeContent) error {
	fields := append([]string{n.Headline}, n.Takeaways...)
	fields = append(fields, n.Drivers...)
	fields = append(fields, n.Limitations...)
	fields = append(fields, n.Recommendations...)
	for _, f := range fields {
		if containsMarkup(f) {
			return fmt.Errorf("narrative content rejected: contains HTML or script-like markup")
		}
	}
	return nil
}

func containsMarkup(s string) bool {
	if s == "" {
		return false
	}
	return htmlTagPattern.MatchString(s) || scriptURIPattern.MatchString(s) || eventHandlerPattern.MatchString(s)
}

var (
	sqlSelectPattern = regexp.MustCompile(`(?i)\b(select|with|insert|update|delete|drop|alter|create)\b[\s\S]{0,80}\b(from|into|table|set)\b`)
	sqlFencePattern  = regexp.MustCompile("(?i)```\\s*sql")
)

// rejectEmbeddedSQL rejects narrative fields that look like SQL statements or
// fenced SQL blocks. Narratives are prose; SQL belongs in the report's SQL field.
func rejectEmbeddedSQL(n *NarrativeContent) error {
	fields := append([]string{n.Headline}, n.Takeaways...)
	fields = append(fields, n.Drivers...)
	fields = append(fields, n.Limitations...)
	fields = append(fields, n.Recommendations...)
	for _, f := range fields {
		if containsSQL(f) {
			return fmt.Errorf("narrative content rejected: contains SQL-like content")
		}
	}
	return nil
}

func containsSQL(s string) bool {
	if s == "" {
		return false
	}
	return sqlFencePattern.MatchString(s) || sqlSelectPattern.MatchString(s)
}

// groundNumericalClaims removes takeaways/drivers whose numeric claims do not
// correspond to any number present in the computed metrics, when metrics are
// available. This is a heuristic (numbers are compared as parsed floats with
// tolerance) intended to catch outright fabricated figures, not to enforce
// exact formatting. When metricsJSON is empty or unparsable, grounding is
// skipped (fallback narrative construction does not need this check; only
// LLM-produced text carrying invented numbers does).
func groundNumericalClaims(n *NarrativeContent, metricsJSON string) {
	metricsJSON = strings.TrimSpace(metricsJSON)
	if metricsJSON == "" {
		return
	}
	known := extractNumbers(metricsJSON)
	if len(known) == 0 {
		return
	}
	n.Takeaways = filterGrounded(n.Takeaways, known)
	n.Drivers = filterGrounded(n.Drivers, known)
}

// filterGrounded keeps only statements whose numeric tokens (if any) are all
// found among known metric values (within a small relative tolerance).
// Statements without numbers pass through unchanged.
func filterGrounded(statements []string, known map[float64]struct{}) []string {
	out := make([]string, 0, len(statements))
	for _, s := range statements {
		nums := extractNumbers(s)
		if len(nums) == 0 {
			out = append(out, s)
			continue
		}
		grounded := true
		for v := range nums {
			if !numberIsKnown(v, known) {
				grounded = false
				break
			}
		}
		if grounded {
			out = append(out, s)
		}
	}
	return out
}

func numberIsKnown(v float64, known map[float64]struct{}) bool {
	// Percentages and small integers (e.g. rankings, counts of items in the
	// takeaway itself) are common and not always literal metric values;
	// only ground values large enough to plausibly be fabricated statistics.
	if v == 0 || (v > -3 && v < 3) {
		return true
	}
	for k := range known {
		if k == 0 {
			continue
		}
		diff := v - k
		if diff < 0 {
			diff = -diff
		}
		tolerance := k * 0.02
		if tolerance < 0 {
			tolerance = -tolerance
		}
		if tolerance < 0.5 {
			tolerance = 0.5
		}
		if diff <= tolerance {
			return true
		}
	}
	return false
}

// extractNumbers pulls numeric tokens out of arbitrary text (including raw
// metrics JSON, where numbers appear as JSON scalars) into a set of float64
// values for comparison.
func extractNumbers(text string) map[float64]struct{} {
	out := map[float64]struct{}{}
	for _, m := range numberInTextPattern.FindAllString(text, -1) {
		clean := numberCleanupPattern.ReplaceAllString(m, "")
		clean = strings.TrimRight(clean, ".")
		if clean == "" || clean == "-" {
			continue
		}
		v, err := strconv.ParseFloat(clean, 64)
		if err != nil {
			continue
		}
		out[v] = struct{}{}
	}
	return out
}

// extractJSON extracts JSON from a response that might contain markdown or other text
func extractJSON(response string) string {
	// Remove markdown code blocks
	response = strings.TrimSpace(response)

	// Try to find JSON object
	jsonPattern := regexp.MustCompile(`(?s)\{.*\}`)
	matches := jsonPattern.FindString(response)
	if matches != "" {
		return matches
	}

	// If no JSON found, try the whole response
	return response
}
