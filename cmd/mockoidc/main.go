// mockoidc serves a local OIDC IdP for browser E2E and manual staging drills.
package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/pgquerynarrative/pgquerynarrative/app/auth/mockoidc"
)

func main() {
	addr := os.Getenv("MOCK_OIDC_ADDR")
	if addr == "" {
		addr = ":9999"
	}
	audience := os.Getenv("MOCK_OIDC_AUDIENCE")
	if audience == "" {
		audience = "pgquerynarrative"
	}
	clientID := os.Getenv("MOCK_OIDC_CLIENT_ID")
	if clientID == "" {
		clientID = "e2e-client"
	}
	srv, err := mockoidc.Start(addr, audience, clientID)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("mock OIDC issuer=%s audience=%s client_id=%s", srv.Issuer, audience, clientID) // #nosec G706 -- startup log of local test-only IdP; values come from env vars set by the operator, not network input.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	_ = srv.Close()
}
