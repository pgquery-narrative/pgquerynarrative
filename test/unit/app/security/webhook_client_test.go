package security_test

import (
	"context"
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
