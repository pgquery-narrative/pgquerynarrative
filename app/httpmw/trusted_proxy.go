// Package httpmw provides HTTP middleware shared by the standalone server and embedders.
package httpmw

import (
	"net"
	"net/http"
	"strings"
)

// TrustedProxyMatcher decides when X-Forwarded-For / X-Real-IP may be trusted.
type TrustedProxyMatcher struct {
	exact map[string]struct{}
	nets  []*net.IPNet
}

// NewTrustedProxyMatcher builds a matcher from exact IPs and CIDR blocks.
func NewTrustedProxyMatcher(addrs []string) *TrustedProxyMatcher {
	m := &TrustedProxyMatcher{exact: make(map[string]struct{})}
	for _, raw := range addrs {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if strings.Contains(raw, "/") {
			if _, network, err := net.ParseCIDR(raw); err == nil {
				m.nets = append(m.nets, network)
			}
			continue
		}
		if ip := net.ParseIP(raw); ip != nil {
			m.exact[ip.String()] = struct{}{}
		}
	}
	return m
}

func (m *TrustedProxyMatcher) contains(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if _, ok := m.exact[ip.String()]; ok {
		return true
	}
	for _, n := range m.nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// ClientIPFromRequest returns the client IP, honoring forwarded headers only from trusted proxies.
func ClientIPFromRequest(r *http.Request, trusted *TrustedProxyMatcher) string {
	remoteHost, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		remoteHost = r.RemoteAddr
	}
	remoteIP := net.ParseIP(remoteHost)
	if trusted != nil && trusted.contains(remoteIP) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			return strings.TrimSpace(strings.Split(xff, ",")[0])
		}
		if xri := r.Header.Get("X-Real-IP"); xri != "" {
			return strings.TrimSpace(xri)
		}
	}
	if remoteIP != nil {
		return remoteIP.String()
	}
	return remoteHost
}
