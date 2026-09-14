package main

import "testing"

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Health and readiness":             "health-and-readiness",
		"Verify result equivalence":        "verify-result-equivalence",
		"`connection_id` resolution":       "connection_id-resolution",
		"Scheduler stuck / duplicate runs": "scheduler-stuck--duplicate-runs", // GitHub keeps the double hyphen
		"Reports and LLM":                  "reports-and-llm",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAnchorExists(t *testing.T) {
	body := "# Title\n\n## Health and readiness {#health-and-readiness}\n\ntext\n\n## Plain Heading\n\n<a name=\"legacy\"></a>\n"
	for _, a := range []string{"health-and-readiness", "plain-heading", "legacy"} {
		if !anchorExists(body, a) {
			t.Errorf("anchorExists(%q) = false, want true", a)
		}
	}
	if anchorExists(body, "nope") {
		t.Errorf("anchorExists(nope) = true, want false")
	}
}

func TestForbiddenVocabRegexes(t *testing.T) {
	hits := map[string]bool{
		"the report requires equivalence Equal today":                true,
		"requires equivalence **Equal**":                             true,
		"we call it an equivalence proof":                            true,
		"pass regression_id in the body":                             true,
		"clone https://github.com/pgquerynarrative/pgquerynarrative": true,
		"needs Go 1.24 or newer":                                     true,
		"a fast dev seed of 8,000 rows":                              true,
	}
	misses := []string{
		"import \"github.com/pgquerynarrative/pgquerynarrative/pkg/narrative\"",
		"pass regression_alert_id in the body",
		"requires Go 1.26+",
		"the 300,000-row seed",
		"result verification, not proof",
	}
	for s := range hits {
		if !anyVocabMatch(s) {
			t.Errorf("expected a forbidden-vocab hit for %q", s)
		}
	}
	for _, s := range misses {
		if anyVocabMatch(s) {
			t.Errorf("unexpected forbidden-vocab hit for %q", s)
		}
	}
}

func anyVocabMatch(s string) bool {
	for _, rule := range forbiddenVocab {
		if rule.re.MatchString(s) {
			return true
		}
	}
	return false
}

func TestFindTableRow(t *testing.T) {
	doc := "intro\n\n| `POSTGRES_IMAGE` | `postgres:16-alpine` | build base |\n| `OTHER` | x | y |\n"
	row := findTableRow(doc, "POSTGRES_IMAGE")
	if row == "" || !contains(row, "postgres:16-alpine") {
		t.Fatalf("findTableRow returned %q", row)
	}
	if findTableRow(doc, "MISSING") != "" {
		t.Fatalf("expected empty row for MISSING")
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (index(s, sub) >= 0) }

func index(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
