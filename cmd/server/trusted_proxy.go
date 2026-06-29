package main

import (
	"net"
	"net/http"
	"strings"
)

type trustedProxyMatcher struct {
	exact map[string]struct{}
	nets  []*net.IPNet
}

func newTrustedProxyMatcher(addrs []string) *trustedProxyMatcher {
	m := &trustedProxyMatcher{exact: make(map[string]struct{})}
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

func (m *trustedProxyMatcher) contains(ip net.IP) bool {
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

func clientIPFromRequest(r *http.Request, trusted *trustedProxyMatcher) string {
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
