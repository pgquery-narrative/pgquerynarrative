package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/test/testhelpers"
)

// TestPilot_OIDCCorporateFlow validates browser OIDC against a mock corporate IdP (PKCE, token exchange, session).
func TestPilot_OIDCCorporateFlow(t *testing.T) {
	ctx := context.Background()
	admin, connStr := pilotPostgres(t, ctx)
	defer admin.Close()

	appPool, err := testhelpers.AppPoolFromAdmin(ctx, admin, connStr)
	if err != nil {
		t.Fatal(err)
	}
	defer appPool.Close()

	mock, err := testhelpers.NewMockOIDCServer("pgquerynarrative", "staging-client")
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	oidc := auth.NewOIDCValidator(auth.OIDCConfig{
		Issuer:   mock.Issuer,
		Audience: "pgquerynarrative",
	})
	sessions := auth.NewSessionManager("staging-session-secret-32-chars!!", time.Hour, false)
	redirectURL := "http://127.0.0.1:8080/auth/callback"
	browser := auth.NewBrowserOIDC(auth.BrowserOIDCConfig{
		Issuer:       mock.Issuer,
		ClientID:     mock.ClientID,
		ClientSecret: "staging-secret",
		RedirectURL:  redirectURL,
		Audience:     "pgquerynarrative",
	}, oidc, sessions)
	browser.SetPKCEStore(auth.NewPKCEStore(appPool))

	staging := auth.ValidateStaging(ctx, auth.StagingConfig{
		Issuer:       mock.Issuer,
		ClientID:     mock.ClientID,
		ClientSecret: "staging-secret",
		RedirectURL:  redirectURL,
		Audience:     "pgquerynarrative",
	}, "staging-session-secret-32-chars!!")
	if !staging.OK {
		t.Fatalf("mock IdP staging validation failed: %v", staging.Issues)
	}

	// Login redirect
	loginReq := httptest.NewRequest(http.MethodGet, "/auth/login", nil)
	loginRec := httptest.NewRecorder()
	browser.LoginHandler(loginRec, loginReq)
	if loginRec.Code != http.StatusFound {
		t.Fatalf("login status=%d", loginRec.Code)
	}
	loc := loginRec.Header().Get("Location")
	if !strings.Contains(loc, "code_challenge=") {
		t.Fatalf("expected PKCE challenge in redirect: %s", loc)
	}

	// Follow authorize redirect on mock IdP; it returns redirect to app callback with code.
	authReq, err := http.NewRequestWithContext(ctx, http.MethodGet, loc, nil)
	if err != nil {
		t.Fatal(err)
	}
	authRec := httptest.NewRecorder()
	mock.Server.Config.Handler.ServeHTTP(authRec, authReq)
	if authRec.Code != http.StatusFound {
		t.Fatalf("authorize status=%d body=%s", authRec.Code, authRec.Body.String())
	}
	callbackLoc := authRec.Header().Get("Location")
	cbReq := httptest.NewRequest(http.MethodGet, callbackLoc, nil)
	cbRec := httptest.NewRecorder()
	browser.CallbackHandler(cbRec, cbReq)
	if cbRec.Code != http.StatusFound {
		t.Fatalf("callback status=%d body=%s", cbRec.Code, cbRec.Body.String())
	}

	sessionReq := httptest.NewRequest(http.MethodGet, "/auth/session", nil)
	for _, c := range cbRec.Result().Cookies() {
		sessionReq.AddCookie(c)
	}
	principal, ok := auth.ValidateSessionCookie(sessionReq, sessions)
	if !ok || principal.UserID != "corp-user-123" {
		t.Fatalf("session principal=%+v ok=%v", principal, ok)
	}

	refreshReq := httptest.NewRequest(http.MethodPost, "/auth/refresh", nil)
	for _, c := range cbRec.Result().Cookies() {
		refreshReq.AddCookie(c)
	}
	refreshRec := httptest.NewRecorder()
	browser.RefreshHandler(refreshRec, refreshReq)
	if refreshRec.Code != http.StatusOK {
		t.Fatalf("refresh status=%d", refreshRec.Code)
	}

	bearer, err := mock.IssueBearerToken("corp-api-user", []string{"admin"})
	if err != nil {
		t.Fatal(err)
	}
	apiReq := httptest.NewRequest(http.MethodGet, "/api/v1/queries/history", nil)
	apiReq.Header.Set("Authorization", "Bearer "+bearer)
	sub, roles, err := oidc.Validate(ctx, bearer)
	if err != nil || sub != "corp-api-user" {
		t.Fatalf("bearer validate: sub=%q err=%v roles=%v", sub, err, roles)
	}
}

// TestPilot_OIDCRealStaging validates a configured corporate IdP when OIDC_STAGING_VALIDATE=1.
func TestPilot_OIDCRealStaging(t *testing.T) {
	if os.Getenv("OIDC_STAGING_VALIDATE") != "1" {
		t.Skip("set OIDC_STAGING_VALIDATE=1 with corporate IdP env vars to run")
	}
	ctx := context.Background()
	report := auth.ValidateStaging(ctx, auth.StagingConfig{
		Issuer:       os.Getenv("SECURITY_OIDC_ISSUER"),
		ClientID:     os.Getenv("SECURITY_OIDC_CLIENT_ID"),
		ClientSecret: os.Getenv("SECURITY_OIDC_CLIENT_SECRET"),
		RedirectURL:  os.Getenv("SECURITY_OIDC_REDIRECT_URL"),
		Audience:     os.Getenv("SECURITY_OIDC_AUDIENCE"),
		JWKSURL:      os.Getenv("SECURITY_OIDC_JWKS_URL"),
	}, os.Getenv("SECURITY_SESSION_SECRET"))
	if !report.OK {
		t.Fatalf("corporate IdP staging validation failed: %v", report.Issues)
	}
	t.Logf("corporate IdP OK: authorize=%s token=%s jwks=%s", report.AuthorizeEndpoint, report.TokenEndpoint, report.JWKSURL)
}
