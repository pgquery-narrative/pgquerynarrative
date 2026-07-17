// Package security provides shared security helpers (URL validation, etc.).
package security

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// ErrWebhookURLNotAllowed indicates a webhook target failed SSRF validation.
var ErrWebhookURLNotAllowed = errors.New("webhook URL is not allowed")

// ValidateWebhookURL ensures destination is a safe external HTTPS URL (blocks private IPs and metadata endpoints).
func ValidateWebhookURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("webhook URL is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid webhook URL: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("webhook URL must use https")
	}
	if u.User != nil {
		return fmt.Errorf("webhook URL must not include credentials")
	}
	host := strings.TrimSpace(u.Hostname())
	if host == "" {
		return fmt.Errorf("webhook URL must include a host")
	}
	lower := strings.ToLower(host)
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") {
		return ErrWebhookURLNotAllowed
	}
	if lower == "metadata.google.internal" || strings.HasSuffix(lower, ".internal") {
		return ErrWebhookURLNotAllowed
	}
	if ip := net.ParseIP(host); ip != nil {
		if isPrivateOrReservedIP(ip) {
			return ErrWebhookURLNotAllowed
		}
		return nil
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("webhook host lookup failed: %w", err)
	}
	for _, ip := range ips {
		if isPrivateOrReservedIP(ip) {
			return ErrWebhookURLNotAllowed
		}
	}
	return nil
}

// ValidateWebhookHostAllowlist ensures the destination host matches an optional allowlist.
func ValidateWebhookHostAllowlist(raw string, allowed []string) error {
	if len(allowed) == 0 {
		return nil
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("invalid webhook URL: %w", err)
	}
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	for _, pattern := range allowed {
		pattern = strings.ToLower(strings.TrimSpace(pattern))
		if pattern == "" {
			continue
		}
		if host == pattern || strings.HasSuffix(host, "."+pattern) {
			return nil
		}
	}
	return fmt.Errorf("webhook host %q is not in the allowlist", host)
}

func normalizeAllowedHosts(hosts []string) []string {
	if len(hosts) == 0 {
		return nil
	}
	out := make([]string, 0, len(hosts))
	for _, h := range hosts {
		h = strings.TrimSpace(h)
		if h != "" {
			out = append(out, h)
		}
	}
	return out
}

func isPrivateOrReservedIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	privateRanges := []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8",
		"169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24",
		"192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24",
		"224.0.0.0/4", "240.0.0.0/4", "::1/128", "fc00::/7", "fe80::/10",
	}
	for _, cidr := range privateRanges {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		if network.Contains(ip) {
			return true
		}
	}
	return false
}
