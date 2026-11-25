package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pgquerynarrative/pgquerynarrative/api/gen/investigations"
	"github.com/pgquerynarrative/pgquerynarrative/internal/auth"
)

// applyInvestigationVisibility is the last gate between a stored investigation
// and whoever is reading it, mirroring applySQLVisibility for reports.go (see
// TestApplySQLVisibility). Every role's outcome is pinned here without a
// database, so the matrix stays obvious even as fields are added.
func TestApplyInvestigationVisibility(t *testing.T) {
	// encoding/json HTML-escapes '<', '>' and '&' by default, so these must
	// avoid them to appear verbatim in a marshaled response for substring
	// comparison below.
	const rawSQL = "SELECT customer_email FROM demo.sales WHERE internal_margin = 0.4"
	const candidateSQL = "SELECT customer_email FROM demo.sales WHERE internal_margin = 0.4 AND 1=1"
	const filterSecret = "secret-filter-literal-9c21"
	const failureSecret = "duplicate key value violates unique constraint secret-detail-77b1"

	ctxAs := func(role, userID string) context.Context {
		return auth.WithPrincipal(context.Background(), auth.Principal{
			UserID: userID, OrgID: auth.DefaultOrganizationID, Role: role,
		})
	}

	failure := "query execution failed: SQLSTATE 23505: " + failureSecret
	fresh := func() *investigations.Investigation {
		candSQL := candidateSQL
		return &investigations.Investigation{
			ID:           "inv-1",
			SQL:          rawSQL,
			Binds:        []string{"captured-bind-secret"},
			CandidateSQL: &candSQL,
			Explain: &investigations.ExplainQueryResult{
				SQL:  rawSQL,
				Plan: map[string]any{"Filter": "(status = '" + filterSecret + "'::text)"},
				Findings: []*investigations.PlanFinding{{
					Message:  "Sequential scan on demo.sales (estimated cost 120.00) — filter: (status = '" + filterSecret + "'::text) — consider a btree index on filtered or joined columns",
					Evidence: []string{"Total Cost=120.00", "Filter=(status = '" + filterSecret + "'::text)"},
				}},
				Diagnosis: &investigations.PlanDiagnosis{
					RootCause: &investigations.PlanDiagnosisCause{
						Evidence: []string{"Sequential scan on demo.sales (estimated cost 120.00) — filter: (status = '" + filterSecret + "'::text)"},
					},
				},
			},
			Comparison: &investigations.ComparePlansResult{
				Before:                  &investigations.ExplainQueryResult{SQL: rawSQL},
				After:                   &investigations.ExplainQueryResult{SQL: candidateSQL},
				ResultEquivalenceStatus: strPtr("Unverified"),
				ResultEquivalenceNotes:  strPtr("sample run failed: invalid input syntax for type integer: \"" + filterSecret + "\""),
			},
			PostgrestRequest: &investigations.PostgRESTRequest{
				Method: "GET", Endpoint: "/sales", Role: "web_anon", Failure: &failure,
			},
			Candidates: []*investigations.InvestigationCandidate{{
				ID:           "cand-1",
				CandidateSQL: candidateSQL,
				Binds:        []string{"candidate-bind-secret"},
				CandidateExplain: &investigations.ExplainQueryResult{
					SQL: candidateSQL,
					Findings: []*investigations.PlanFinding{{
						Message: "Sequential scan on demo.sales (estimated cost 10.00) — filter: (status = '" + filterSecret + "'::text) — likely acceptable for small or unfiltered scans",
					}},
				},
			}},
		}
	}

	markers := []string{rawSQL, candidateSQL, filterSecret, failureSecret, "captured-bind-secret", "candidate-bind-secret"}
	presentMarkers := func(inv *investigations.Investigation) []string {
		b, err := json.Marshal(inv)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var found []string
		for _, m := range markers {
			if strings.Contains(string(b), m) {
				found = append(found, m)
			}
		}
		return found
	}
	missingOf := func(all, present []string) []string {
		have := map[string]bool{}
		for _, p := range present {
			have[p] = true
		}
		var missing []string
		for _, m := range all {
			if !have[m] {
				missing = append(missing, m)
			}
		}
		return missing
	}

	t.Run("a nil investigation is a no-op", func(t *testing.T) {
		applyInvestigationVisibility(ctxAs(auth.RoleAdmin, "u1"), nil) // must not panic
	})

	t.Run("admins and analysts keep every field", func(t *testing.T) {
		for _, role := range []string{auth.RolePlatformAdmin, auth.RoleTenantAdmin, auth.RoleAnalyst} {
			inv := fresh()
			applyInvestigationVisibility(ctxAs(role, "u1"), inv)
			if got := presentMarkers(inv); len(got) != len(markers) {
				t.Errorf("%s: expected every marker retained, missing %v", role, missingOf(markers, got))
			}
		}
	})

	t.Run("viewers and unknown roles lose every sensitive field", func(t *testing.T) {
		for _, role := range []string{auth.RoleViewer, "some-future-role"} {
			inv := fresh()
			applyInvestigationVisibility(ctxAs(role, "u1"), inv)
			if got := presentMarkers(inv); len(got) != 0 {
				t.Errorf("role %q: markers survived redaction: %v", role, got)
			}
			if inv.SQL != "" || inv.Binds != nil || inv.CandidateSQL != nil {
				t.Errorf("role %q: top-level sensitive fields not cleared: sql=%q binds=%v candidateSQL=%v", role, inv.SQL, inv.Binds, inv.CandidateSQL)
			}
			if inv.PostgrestRequest == nil || inv.PostgrestRequest.Failure != nil {
				t.Errorf("role %q: PostgrestRequest.Failure must be cleared, got %+v", role, inv.PostgrestRequest)
			}
			if inv.Explain.Plan != nil || inv.Comparison.Before.Plan != nil {
				t.Errorf("role %q: raw EXPLAIN Plan must be cleared, got Explain.Plan=%v Comparison.Before.Plan=%v", role, inv.Explain.Plan, inv.Comparison.Before.Plan)
			}
			if inv.Comparison.ResultEquivalenceNotes == nil || strings.Contains(*inv.Comparison.ResultEquivalenceNotes, filterSecret) {
				t.Errorf("role %q: Unverified equivalence notes must not quote the literal, got %v", role, inv.Comparison.ResultEquivalenceNotes)
			}
			for _, c := range inv.Candidates {
				if c.CandidateSQL != "" || c.Binds != nil {
					t.Errorf("role %q: candidate sensitive fields not cleared: %+v", role, c)
				}
			}
		}
	})

	t.Run("an absent role resolves to the admin default", func(t *testing.T) {
		inv := fresh()
		applyInvestigationVisibility(context.Background(), inv)
		if got := presentMarkers(inv); len(got) != len(markers) {
			t.Errorf("expected every marker retained for the admin-default context, missing %v", missingOf(markers, got))
		}
	})
}

// Investigations stored before extractFilterColumns learned to skip string
// literals carry literal tokens in RelatedColumns and IndexAdvice. A
// restricted view must not echo them: only columns the stored plan itself
// proves (re-derived with the current extractor) survive, and advice built
// from any other "column" is withheld whole (its DDL and benefit text embed
// them). Authorized roles keep the stored evidence untouched.
func TestApplyInvestigationVisibilityDropsLegacyLiteralColumns(t *testing.T) {
	ctxAs := func(role string) context.Context {
		return auth.WithPrincipal(context.Background(), auth.Principal{UserID: "u", OrgID: auth.DefaultOrganizationID, Role: role})
	}
	var plan any
	if err := json.Unmarshal([]byte(`[{"Plan":{"Node Type":"Seq Scan","Relation Name":"sales","Schema":"demo",
		"Filter":"((sales_rep)::text = 'bearersecret = privatevalue'::text)"}}]`), &plan); err != nil {
		t.Fatal(err)
	}
	tainted := []string{"sales_rep", "bearersecret", "privatevalue"}
	explain := func(withPlan bool) *investigations.ExplainQueryResult {
		e := &investigations.ExplainQueryResult{Findings: []*investigations.PlanFinding{{
			NodeType: "Seq Scan", Message: "Seq Scan on demo.sales",
			RelatedColumns: append([]string(nil), tainted...),
			IndexAdvice: &investigations.IndexAdvice{
				RelatedColumns: append([]string(nil), tainted...),
				CandidateDdl:   strPtr("CREATE INDEX ON demo.sales (sales_rep, bearersecret, privatevalue);"),
			},
		}}}
		if withPlan {
			e.Plan = plan
		}
		return e
	}
	fresh := func() *investigations.Investigation {
		return &investigations.Investigation{
			ID: "inv-legacy", Explain: explain(true), CandidateExplain: explain(false),
			Comparison: &investigations.ComparePlansResult{Before: explain(true), After: explain(true)},
			Candidates: []*investigations.InvestigationCandidate{{ID: "c1", CandidateExplain: explain(true)}},
		}
	}

	inv := fresh()
	fp := "0123abcd"
	inv.QueryFingerprint = &fp
	applyInvestigationVisibility(ctxAs(auth.RoleViewer), inv)
	if inv.QueryFingerprint != nil {
		t.Fatal("viewer response kept the SQL-text fingerprint (a guessing oracle for its literals)")
	}
	body, _ := json.Marshal(inv)
	for _, secret := range []string{"bearersecret", "privatevalue"} {
		if strings.Contains(string(body), secret) {
			t.Fatalf("viewer response still carries legacy literal token %q: %s", secret, body)
		}
	}
	if got := inv.Explain.Findings[0].RelatedColumns; len(got) != 1 || got[0] != "sales_rep" {
		t.Fatalf("plan-proven column must survive for viewers, got %v", got)
	}
	if got := inv.CandidateExplain.Findings[0].RelatedColumns; len(got) != 0 {
		t.Fatalf("without a stored plan nothing can be proven; got %v", got)
	}

	for _, role := range []string{auth.RoleAnalyst, auth.RoleAdmin} {
		inv := fresh()
		applyInvestigationVisibility(ctxAs(role), inv)
		if len(inv.Explain.Findings[0].RelatedColumns) != 3 || inv.Explain.Findings[0].IndexAdvice == nil {
			t.Fatalf("%s must keep stored evidence untouched", role)
		}
	}
}
