package security

import "testing"

func TestValidateWebhookURL_BlocksPrivate(t *testing.T) {
	cases := []string{
		"http://example.com/hook",
		"https://127.0.0.1/hook",
		"https://10.0.0.1/hook",
		"https://localhost/hook",
	}
	for _, u := range cases {
		if err := ValidateWebhookURL(u); err == nil {
			t.Fatalf("expected error for %q", u)
		}
	}
}

func TestValidateWebhookURL_AllowsPublicHTTPS(t *testing.T) {
	if err := ValidateWebhookURL("https://8.8.8.8/pgqn"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
