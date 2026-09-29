package main

import (
	"net/http"

	"github.com/pgquerynarrative/pgquerynarrative/internal/middleware"
)

type trustedProxyMatcher = middleware.TrustedProxyMatcher

func newTrustedProxyMatcher(addrs []string) *trustedProxyMatcher {
	return middleware.NewTrustedProxyMatcher(addrs)
}

func clientIPFromRequest(r *http.Request, trusted *trustedProxyMatcher) string {
	return middleware.ClientIPFromRequest(r, trusted)
}
