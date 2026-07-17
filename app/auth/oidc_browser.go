package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// BrowserOIDCConfig configures authorization-code + PKCE browser login.
type BrowserOIDCConfig struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	Audience     string
}

// BrowserOIDC handles browser login, callback, and logout.
type BrowserOIDC struct {
	cfg        BrowserOIDCConfig
	oidc       *OIDCValidator
	session    *SessionManager
	membership *MembershipStore
	pkce       *PKCEStore
	client     *http.Client
	mu         sync.Mutex
	pending    map[string]pkceState // in-memory fallback when pkce store is nil
}

type pkceState struct {
	Verifier  string
	ExpiresAt time.Time
}

// NewBrowserOIDC returns a browser OIDC handler when client credentials are configured.
func NewBrowserOIDC(cfg BrowserOIDCConfig, oidc *OIDCValidator, session *SessionManager) *BrowserOIDC {
	if strings.TrimSpace(cfg.ClientID) == "" || strings.TrimSpace(cfg.Issuer) == "" || session == nil || !session.Enabled() {
		return nil
	}
	return &BrowserOIDC{
		cfg:     cfg,
		oidc:    oidc,
		session: session,
		client:  &http.Client{Timeout: 15 * time.Second},
		pending: make(map[string]pkceState),
	}
}

// SetMembershipStore attaches organization membership resolution for browser login.
func (b *BrowserOIDC) SetMembershipStore(store *MembershipStore) {
	if b != nil {
		b.membership = store
	}
}

// SetPKCEStore enables multi-replica-safe PKCE state persistence.
func (b *BrowserOIDC) SetPKCEStore(store *PKCEStore) {
	if b != nil {
		b.pkce = store
	}
}

// Enabled reports whether browser OIDC login is available.
func (b *BrowserOIDC) Enabled() bool {
	return b != nil && b.session != nil && b.session.Enabled()
}

// LoginHandler redirects the browser to the OIDC provider authorize endpoint.
func (b *BrowserOIDC) LoginHandler(w http.ResponseWriter, r *http.Request) {
	if !b.Enabled() {
		http.Error(w, "browser OIDC not configured", http.StatusNotFound)
		return
	}
	state, err := randomToken(24)
	if err != nil {
		http.Error(w, "failed to start login", http.StatusInternalServerError)
		return
	}
	verifier, challenge, err := NewPKCEVerifier()
	if err != nil {
		http.Error(w, "failed to start login", http.StatusInternalServerError)
		return
	}
	if b.pkce != nil {
		if err := b.pkce.Save(r.Context(), state, verifier, 10*time.Minute); err != nil {
			http.Error(w, "failed to start login", http.StatusInternalServerError)
			return
		}
	} else {
		b.mu.Lock()
		b.pending[state] = pkceState{Verifier: verifier, ExpiresAt: time.Now().Add(10 * time.Minute)}
		b.mu.Unlock()
	}

	q := url.Values{}
	q.Set("client_id", b.cfg.ClientID)
	q.Set("response_type", "code")
	q.Set("scope", "openid profile email")
	q.Set("redirect_uri", b.cfg.RedirectURL)
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	authorizeURL, _, _ := OIDCEndpoints(r.Context(), b.cfg.Issuer, b.client)
	authURL := authorizeURL + "?" + q.Encode()
	http.Redirect(w, r, authURL, http.StatusFound)
}

// CallbackHandler completes the authorization code exchange and issues a session cookie.
func (b *BrowserOIDC) CallbackHandler(w http.ResponseWriter, r *http.Request) {
	if !b.Enabled() {
		http.Error(w, "browser OIDC not configured", http.StatusNotFound)
		return
	}
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	if code == "" || state == "" {
		http.Error(w, "missing code or state", http.StatusBadRequest)
		return
	}
	var verifier string
	var ok bool
	if b.pkce != nil {
		verifier, ok = b.pkce.Consume(r.Context(), state)
	} else {
		b.mu.Lock()
		pending, found := b.pending[state]
		delete(b.pending, state)
		b.mu.Unlock()
		ok = found && time.Now().Before(pending.ExpiresAt)
		verifier = pending.Verifier
	}
	if !ok || verifier == "" {
		http.Error(w, "invalid or expired login state", http.StatusBadRequest)
		return
	}

	tokenResp, err := b.exchangeCode(r.Context(), code, verifier)
	if err != nil {
		http.Error(w, "token exchange failed", http.StatusUnauthorized)
		return
	}
	idToken, _ := tokenResp["id_token"].(string)
	if idToken == "" {
		if access, _ := tokenResp["access_token"].(string); access != "" && b.oidc != nil && b.oidc.Enabled() {
			idToken = access
		}
	}
	if idToken == "" || b.oidc == nil || !b.oidc.Enabled() {
		http.Error(w, "missing id_token", http.StatusUnauthorized)
		return
	}
	sub, roles, err := b.oidc.Validate(r.Context(), idToken)
	if err != nil || strings.TrimSpace(sub) == "" {
		http.Error(w, "invalid id_token", http.StatusUnauthorized)
		return
	}
	role := RoleAnalyst
	if len(roles) > 0 {
		role = mapOIDCRole(roles[0])
	}
	orgID := DefaultOrgID()
	if b.membership != nil {
		p, resolveErr := b.membership.ResolveFromGroupClaims(r.Context(), sub, PreferredOrgFromRequest(r), role, roles)
		if resolveErr != nil {
			http.Error(w, "no organization membership", http.StatusForbidden)
			return
		}
		orgID = p.OrgID
		role = p.Role
	}
	ttl := 8 * time.Hour
	if b.session != nil && b.session.TTL() > 0 {
		ttl = b.session.TTL()
	}
	refreshToken, _ := tokenResp["refresh_token"].(string)
	if err := b.session.Issue(w, Session{
		UserID:       sub,
		OrgID:        orgID,
		Role:         role,
		ExpiresAt:    time.Now().UTC().Add(ttl),
		RefreshToken: refreshToken,
	}); err != nil {
		http.Error(w, "failed to create session", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

// RefreshHandler refreshes the browser session, using the OIDC refresh token when near expiry.
func (b *BrowserOIDC) RefreshHandler(w http.ResponseWriter, r *http.Request) {
	if !b.Enabled() {
		http.Error(w, "browser OIDC not configured", http.StatusNotFound)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s, err := b.session.Read(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	ttl := b.session.TTL()
	if ttl <= 0 {
		ttl = 8 * time.Hour
	}
	renew := s.ExpiresAt.Sub(time.Now().UTC()) < ttl/4
	if renew {
		if strings.TrimSpace(s.RefreshToken) == "" {
			b.session.Clear(w)
			http.Error(w, "session refresh required", http.StatusUnauthorized)
			return
		}
		tokenResp, refreshErr := b.refreshTokens(r.Context(), s.RefreshToken)
		if refreshErr != nil {
			b.session.Clear(w)
			http.Error(w, "provider refresh failed", http.StatusUnauthorized)
			return
		}
		if idToken, _ := tokenResp["id_token"].(string); idToken != "" && b.oidc != nil && b.oidc.Enabled() {
			if sub, roles, validateErr := b.oidc.Validate(r.Context(), idToken); validateErr == nil && strings.TrimSpace(sub) != "" {
				s.UserID = sub
				if len(roles) > 0 {
					s.Role = mapOIDCRole(roles[0])
				}
			}
		}
		if rt, _ := tokenResp["refresh_token"].(string); rt != "" {
			s.RefreshToken = rt
		}
	}
	if b.membership != nil {
		p, resolveErr := b.membership.ResolvePrincipal(r.Context(), s.UserID, s.OrgID, s.Role)
		if resolveErr != nil {
			b.session.Clear(w)
			http.Error(w, "no organization membership", http.StatusForbidden)
			return
		}
		s.OrgID = p.OrgID
		s.Role = p.Role
	}
	s.ExpiresAt = time.Now().UTC().Add(ttl)
	if err := b.session.Issue(w, *s); err != nil {
		http.Error(w, "failed to refresh session", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"refreshed":true}`))
}

func (b *BrowserOIDC) refreshTokens(ctx context.Context, refreshToken string) (map[string]interface{}, error) {
	_, tokenURL, _ := OIDCEndpoints(ctx, b.cfg.Issuer, b.client)
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", b.cfg.ClientID)
	if b.cfg.ClientSecret != "" {
		form.Set("client_secret", b.cfg.ClientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("refresh token status %d", resp.StatusCode)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// LogoutHandler clears the browser session.
func (b *BrowserOIDC) LogoutHandler(w http.ResponseWriter, r *http.Request) {
	if !b.Enabled() {
		http.Error(w, "browser OIDC not configured", http.StatusNotFound)
		return
	}
	b.session.Clear(w)
	http.Redirect(w, r, "/", http.StatusFound)
}

func (b *BrowserOIDC) exchangeCode(ctx context.Context, code, verifier string) (map[string]interface{}, error) {
	_, tokenURL, _ := OIDCEndpoints(ctx, b.cfg.Issuer, b.client)
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", b.cfg.RedirectURL)
	form.Set("client_id", b.cfg.ClientID)
	form.Set("code_verifier", verifier)
	if b.cfg.ClientSecret != "" {
		form.Set("client_secret", b.cfg.ClientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("token endpoint status %d", resp.StatusCode)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ValidateSessionCookie returns principal fields from a browser session cookie.
func ValidateSessionCookie(r *http.Request, sessions *SessionManager) (Principal, bool) {
	if sessions == nil || !sessions.Enabled() {
		return Principal{}, false
	}
	s, err := sessions.Read(r)
	if err != nil {
		return Principal{}, false
	}
	return Principal{UserID: s.UserID, OrgID: s.OrgID, Role: s.Role}, true
}

// ErrBrowserOIDCNotConfigured indicates browser OIDC is unavailable.
var ErrBrowserOIDCNotConfigured = errors.New("browser OIDC not configured")
