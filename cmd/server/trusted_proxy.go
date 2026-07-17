package main

import (
	"net/http"

	"github.com/pgquerynarrative/pgquerynarrative/app/httpmw"
)

type trustedProxyMatcher = httpmw.TrustedProxyMatcher

func newTrustedProxyMatcher(addrs []string) *trustedProxyMatcher {
	return httpmw.NewTrustedProxyMatcher(addrs)
}

func clientIPFromRequest(r *http.Request, trusted *trustedProxyMatcher) string {
	return httpmw.ClientIPFromRequest(r, trusted)
}
