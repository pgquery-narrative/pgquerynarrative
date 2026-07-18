package auth

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/sync/singleflight"
)

const (
	maxJWKSBodyBytes      = 1 << 20 // 1 MiB
	oidcClockSkew         = 2 * time.Minute
	jwksCacheTTL          = time.Hour
	strictRequireAudience = true
)

// OIDCConfig holds optional OIDC JWT validation settings.
type OIDCConfig struct {
	Issuer     string
	Audience   string
	JWKSURL    string
	StrictMode bool // When true: require aud/exp/iat/sub, reject missing kid, no single-key fallback.
}

// OIDCValidator validates Bearer JWTs from an OIDC provider.
type OIDCValidator struct {
	cfg        OIDCConfig
	httpClient *http.Client
	mu         sync.RWMutex
	keys       map[string]*rsa.PublicKey
	fetched    time.Time
	jwksSF     singleflight.Group
}

// NewOIDCValidator returns a validator when issuer is configured.
func NewOIDCValidator(cfg OIDCConfig) *OIDCValidator {
	if strings.TrimSpace(cfg.Issuer) == "" {
		return nil
	}
	jwks := strings.TrimSpace(cfg.JWKSURL)
	if jwks == "" {
		jwks = strings.TrimRight(cfg.Issuer, "/") + "/.well-known/jwks.json"
	}
	return &OIDCValidator{
		cfg: OIDCConfig{
			Issuer:     strings.TrimSpace(cfg.Issuer),
			Audience:   strings.TrimSpace(cfg.Audience),
			JWKSURL:    jwks,
			StrictMode: cfg.StrictMode,
		},
		httpClient: &http.Client{Timeout: 10 * time.Second},
		keys:       make(map[string]*rsa.PublicKey),
	}
}

// Enabled reports whether OIDC validation is configured.
func (v *OIDCValidator) Enabled() bool {
	return v != nil && v.cfg.Issuer != ""
}

// Validate parses and validates a JWT, returning subject and roles.
func (v *OIDCValidator) Validate(ctx context.Context, token string) (subject string, roles []string, err error) {
	if !v.Enabled() {
		return "", nil, errors.New("oidc not configured")
	}
	keyFunc := func(t *jwt.Token) (interface{}, error) {
		if t.Method.Alg() != jwt.SigningMethodRS256.Alg() {
			return nil, fmt.Errorf("unexpected signing method: %s", t.Method.Alg())
		}
		kid, _ := t.Header["kid"].(string)
		kid = strings.TrimSpace(kid)
		if kid == "" && (v.cfg.StrictMode || strictRequireAudience) {
			return nil, errors.New("missing kid in token header")
		}
		return v.publicKey(ctx, kid)
	}
	opts := []jwt.ParserOption{
		jwt.WithIssuer(v.cfg.Issuer),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(oidcClockSkew),
	}
	if v.cfg.Audience != "" {
		opts = append(opts, jwt.WithAudience(v.cfg.Audience))
	} else if v.cfg.StrictMode {
		return "", nil, errors.New("audience is required in strict mode")
	}
	parsed, err := jwt.Parse(token, keyFunc, opts...)
	if err != nil {
		return "", nil, err
	}
	if !parsed.Valid {
		return "", nil, errors.New("invalid token")
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return "", nil, errors.New("invalid claims")
	}
	sub, _ := claims["sub"].(string)
	sub = strings.TrimSpace(sub)
	if sub == "" {
		return "", nil, errors.New("missing subject")
	}
	if v.cfg.Audience != "" && !claimAudienceMatches(claims, v.cfg.Audience) {
		return "", nil, errors.New("audience mismatch")
	}
	if azp, ok := claims["azp"].(string); ok && strings.TrimSpace(azp) != "" && v.cfg.Audience != "" {
		if strings.TrimSpace(azp) != v.cfg.Audience && !claimAudienceMatches(claims, v.cfg.Audience) {
			// azp may equal client_id; when audience is configured accept azp == audience as well.
			_ = azp
		}
	}
	roles = claimRoles(claims)
	return sub, roles, nil
}

func claimAudienceMatches(claims jwt.MapClaims, aud string) bool {
	switch v := claims["aud"].(type) {
	case string:
		return v == aud
	case []interface{}:
		for _, item := range v {
			if s, ok := item.(string); ok && s == aud {
				return true
			}
		}
	}
	return false
}

func claimRoles(claims jwt.MapClaims) []string {
	if r, ok := claims["roles"].([]interface{}); ok {
		out := make([]string, 0, len(r))
		for _, item := range r {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	if g, ok := claims["groups"].([]interface{}); ok {
		out := make([]string, 0, len(g))
		for _, item := range g {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

type jwksResponse struct {
	Keys []jwkKey `json:"keys"`
}

type jwkKey struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	N   string `json:"n"`
	E   string `json:"e"`
}

func (v *OIDCValidator) publicKey(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	v.mu.RLock()
	if key, ok := v.keys[kid]; ok && time.Since(v.fetched) < jwksCacheTTL {
		v.mu.RUnlock()
		return key, nil
	}
	v.mu.RUnlock()
	if err := v.refreshJWKS(ctx); err != nil {
		return nil, err
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	if kid == "" {
		return nil, errors.New("missing kid in token header")
	}
	if key, ok := v.keys[kid]; ok {
		return key, nil
	}
	// Never fall back to a single key in strict mode; never accept unknown kids.
	return nil, fmt.Errorf("jwks key %q not found", kid)
}

func (v *OIDCValidator) refreshJWKS(ctx context.Context) error {
	_, err, _ := v.jwksSF.Do("jwks", func() (interface{}, error) {
		return nil, v.fetchJWKS(ctx)
	})
	return err
}

func (v *OIDCValidator) fetchJWKS(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.cfg.JWKSURL, nil)
	if err != nil {
		return err
	}
	resp, err := v.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("jwks fetch status %d", resp.StatusCode)
	}
	limited := io.LimitReader(resp.Body, maxJWKSBodyBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return err
	}
	if len(body) > maxJWKSBodyBytes {
		return errors.New("jwks response too large")
	}
	var parsed jwksResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return err
	}
	keys := make(map[string]*rsa.PublicKey)
	for _, k := range parsed.Keys {
		if k.Kty != "RSA" || k.N == "" || k.E == "" || strings.TrimSpace(k.Kid) == "" {
			continue
		}
		pub, err := rsaPublicKeyFromJWK(k.N, k.E)
		if err != nil {
			continue
		}
		keys[k.Kid] = pub
	}
	if len(keys) == 0 {
		return errors.New("no RSA keys in JWKS")
	}
	v.mu.Lock()
	v.keys = keys
	v.fetched = time.Now()
	v.mu.Unlock()
	return nil
}

func rsaPublicKeyFromJWK(nB64, eB64 string) (*rsa.PublicKey, error) {
	nb, err := base64.RawURLEncoding.DecodeString(nB64)
	if err != nil {
		return nil, err
	}
	eb, err := base64.RawURLEncoding.DecodeString(eB64)
	if err != nil {
		return nil, err
	}
	var e int
	for _, b := range eb {
		e = e<<8 + int(b)
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: e}, nil
}
