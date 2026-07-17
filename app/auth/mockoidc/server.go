// Package mockoidc provides a minimal OIDC IdP for staging validation and browser E2E tests.
package mockoidc

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Server simulates a corporate OIDC IdP with discovery, JWKS, and token endpoints.
type Server struct {
	HTTP     *http.Server
	Issuer   string
	Audience string
	ClientID string
	Kid      string
	private  *rsa.PrivateKey
	mu       sync.Mutex
	codes    map[string]string
	listener net.Listener
}

// New creates an OIDC mock that can be served via httptest or Start.
func New(audience, clientID string) (*Server, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	return &Server{
		Audience: audience,
		ClientID: clientID,
		Kid:      "mock-kid-1",
		private:  key,
		codes:    make(map[string]string),
	}, nil
}

// Handler returns the OIDC HTTP handler. Set Issuer before serving if not using Start.
func (m *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", m.handleDiscovery)
	mux.HandleFunc("/.well-known/jwks.json", m.handleJWKS)
	mux.HandleFunc("/oauth/authorize", m.handleAuthorize)
	mux.HandleFunc("/oauth/token", m.handleToken)
	return mux
}

// Start listens on addr (e.g. ":9999") and serves OIDC endpoints until Close is called.
func Start(addr, audience, clientID string) (*Server, error) {
	m, err := New(audience, clientID)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	m.listener = ln
	m.HTTP = &http.Server{
		Handler:           m.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	m.Issuer = "http://" + ln.Addr().String()
	go func() { _ = m.HTTP.Serve(ln) }()
	return m, nil
}

// Close shuts down the mock IdP.
func (m *Server) Close() error {
	if m == nil || m.HTTP == nil {
		return nil
	}
	err := m.HTTP.Close()
	if m.listener != nil {
		_ = m.listener.Close()
	}
	return err
}

func (m *Server) handleDiscovery(w http.ResponseWriter, _ *http.Request) {
	_ = json.NewEncoder(w).Encode(map[string]string{
		"issuer":                 m.Issuer,
		"authorization_endpoint": m.Issuer + "/oauth/authorize",
		"token_endpoint":         m.Issuer + "/oauth/token",
		"jwks_uri":               m.Issuer + "/.well-known/jwks.json",
	})
}

func (m *Server) handleJWKS(w http.ResponseWriter, _ *http.Request) {
	n := base64.RawURLEncoding.EncodeToString(m.private.PublicKey.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString([]byte{1, 0, 1})
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"keys": []map[string]string{
			{"kty": "RSA", "kid": m.Kid, "alg": "RS256", "use": "sig", "n": n, "e": e},
		},
	})
}

func (m *Server) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	state := q.Get("state")
	redirectURI := q.Get("redirect_uri")
	codeChallenge := q.Get("code_challenge")
	if state == "" || redirectURI == "" || codeChallenge == "" {
		http.Error(w, "missing params", http.StatusBadRequest)
		return
	}
	code := "mock-auth-code-" + state
	m.mu.Lock()
	m.codes[code] = q.Get("code_challenge")
	m.mu.Unlock()
	u, err := url.Parse(redirectURI)
	if err != nil {
		http.Error(w, "bad redirect", http.StatusBadRequest)
		return
	}
	vals := u.Query()
	vals.Set("code", code)
	vals.Set("state", state)
	u.RawQuery = vals.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (m *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	code := r.Form.Get("code")
	verifier := r.Form.Get("code_verifier")
	m.mu.Lock()
	_, ok := m.codes[code]
	m.mu.Unlock()
	if !ok || verifier == "" {
		http.Error(w, "invalid code", http.StatusUnauthorized)
		return
	}
	idToken, err := m.signIDToken("corp-user-123", []string{"analyst"})
	if err != nil {
		http.Error(w, "token issue failed", http.StatusInternalServerError)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"access_token":  idToken,
		"id_token":      idToken,
		"refresh_token": "mock-refresh-token",
		"token_type":    "Bearer",
		"expires_in":    3600,
	})
}

func (m *Server) signIDToken(sub string, roles []string) (string, error) {
	claims := jwt.MapClaims{
		"sub":   sub,
		"iss":   m.Issuer,
		"aud":   m.Audience,
		"roles": roles,
		"exp":   time.Now().Add(time.Hour).Unix(),
		"iat":   time.Now().Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = m.Kid
	return token.SignedString(m.private)
}

// IssueBearerToken returns a signed JWT for API bearer auth tests.
func (m *Server) IssueBearerToken(sub string, roles []string) (string, error) {
	if sub == "" {
		sub = "corp-user-123"
	}
	return m.signIDToken(sub, roles)
}

// AuthorizeURL builds the login redirect URL for manual debugging.
func (m *Server) AuthorizeURL(redirectURI, state, challenge string) string {
	return fmt.Sprintf("%s/oauth/authorize?client_id=%s&response_type=code&redirect_uri=%s&state=%s&code_challenge=%s&code_challenge_method=S256",
		m.Issuer, url.QueryEscape(m.ClientID), url.QueryEscape(redirectURI), url.QueryEscape(state), url.QueryEscape(challenge))
}

// BindIssuer sets Issuer from an httptest server URL (trailing slash trimmed).
func (m *Server) BindIssuer(baseURL string) {
	m.Issuer = strings.TrimRight(baseURL, "/")
}
