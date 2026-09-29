package security

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const maxWebhookResponseBytes = 1 << 20

// WebhookClient delivers signed webhook payloads with SSRF protections at dial time.
type WebhookClient struct {
	httpClient   *http.Client
	secret       []byte
	allowedHosts []string
}

// NewWebhookClient returns a hardened webhook HTTP client.
// allowedHosts, when non-empty, restricts destinations to matching hostnames (suffix match).
func NewWebhookClient(secret string, timeout time.Duration, allowedHosts ...string) *WebhookClient {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			if port != "443" && port != "8443" {
				return nil, fmt.Errorf("webhook port %q is not allowed", port)
			}
			if ip := net.ParseIP(host); ip != nil {
				if isPrivateOrReservedIP(ip) {
					return nil, ErrWebhookURLNotAllowed
				}
				d := net.Dialer{Timeout: timeout}
				return d.DialContext(ctx, network, addr)
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			for _, rec := range ips {
				if isPrivateOrReservedIP(rec.IP) {
					return nil, ErrWebhookURLNotAllowed
				}
			}
			d := net.Dialer{Timeout: timeout}
			return d.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
		},
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: timeout,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return &WebhookClient{
		secret:       []byte(strings.TrimSpace(secret)),
		allowedHosts: normalizeAllowedHosts(allowedHosts),
		httpClient: &http.Client{
			Timeout:   timeout,
			Transport: transport,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// DeliveryResult captures webhook HTTP outcome.
type DeliveryResult struct {
	StatusCode    int
	ResponseBytes int
}

// WebhookSignature returns the HMAC signature for timestamp, delivery ID, and payload.
func WebhookSignature(secret, timestamp, deliveryID string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(strings.TrimSpace(secret)))
	_, _ = mac.Write([]byte(timestamp))
	_, _ = mac.Write([]byte("."))
	_, _ = mac.Write([]byte(deliveryID))
	_, _ = mac.Write([]byte("."))
	_, _ = mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// PostJSON validates the URL, signs the payload, and POSTs JSON to the destination.
func (c *WebhookClient) PostJSON(ctx context.Context, destination string, deliveryID string, payload map[string]any) (*DeliveryResult, error) {
	if err := ValidateWebhookURL(destination); err != nil {
		return nil, err
	}
	if err := ValidateWebhookHostAllowlist(destination, c.allowedHosts); err != nil {
		return nil, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, destination, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	timestamp := time.Now().UTC().Format(time.RFC3339)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-PGQN-Delivery-ID", deliveryID)
	req.Header.Set("X-PGQN-Timestamp", timestamp)
	if len(c.secret) > 0 {
		req.Header.Set("X-PGQN-Signature", WebhookSignature(string(c.secret), timestamp, deliveryID, body))
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxWebhookResponseBytes))
	return &DeliveryResult{StatusCode: resp.StatusCode, ResponseBytes: len(respBody)}, nil
}
