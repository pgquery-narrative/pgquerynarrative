package security_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pgquerynarrative/pgquerynarrative/app/security"
)

func TestWebhookClient_BlocksPrivateIP(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv.Listener = ln
	srv.Start()
	defer srv.Close()

	client := security.NewWebhookClient("secret", 2*time.Second)
	_, err = client.PostJSON(context.Background(), srv.URL, "delivery-1", map[string]any{"ok": true})
	if err == nil {
		t.Fatal("expected private webhook URL to be blocked")
	}
}

func TestWebhookSignature_BindsTimestampDeliveryAndPayload(t *testing.T) {
	body := []byte(`{"report_id":"r1"}`)
	got := security.WebhookSignature("secret", "2026-07-17T00:00:00Z", "delivery-1", body)

	mac := hmac.New(sha256.New, []byte("secret"))
	_, _ = mac.Write([]byte("2026-07-17T00:00:00Z.delivery-1."))
	_, _ = mac.Write(body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if got != want {
		t.Fatalf("signature = %q, want %q", got, want)
	}

	changed := security.WebhookSignature("secret", "2026-07-17T00:00:00Z", "delivery-2", body)
	if changed == got {
		t.Fatal("signature should change when delivery id changes")
	}
}
