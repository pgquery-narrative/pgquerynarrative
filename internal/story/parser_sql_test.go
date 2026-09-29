package story

import "testing"

func TestRejectEmbeddedSQL(t *testing.T) {
	n := &NarrativeContent{
		Headline:  "ok",
		Takeaways: []string{"SELECT * FROM demo.sales WHERE x = 1"},
	}
	if err := rejectEmbeddedSQL(n); err == nil {
		t.Fatal("expected SQL rejection")
	}
	n.Takeaways = []string{"Revenue grew 10%"}
	if err := rejectEmbeddedSQL(n); err != nil {
		t.Fatalf("unexpected rejection: %v", err)
	}
}
