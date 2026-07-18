package auth_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
)

func TestNewSessionManager_EmptySecretDisabled(t *testing.T) {
	if mgr := auth.NewSessionManager("  ", time.Hour, false); mgr != nil {
		t.Errorf("NewSessionManager with blank secret = %v, want nil", mgr)
	}

	mgr := auth.NewSessionManager("secret", 0, false)
	if mgr == nil {
		t.Fatal("expected non-nil manager")
	}
	if mgr.TTL() != 8*time.Hour {
		t.Errorf("TTL() = %v, want default 8h when ttl <= 0", mgr.TTL())
	}
}

func TestSessionManager_NilReceiver(t *testing.T) {
	var mgr *auth.SessionManager
	if mgr.Enabled() {
		t.Error("nil manager Enabled() = true, want false")
	}
	if mgr.TTL() != 0 {
		t.Errorf("nil manager TTL() = %v, want 0", mgr.TTL())
	}
	if err := mgr.Issue(httptest.NewRecorder(), auth.Session{}); err == nil {
		t.Error("nil manager Issue() error = nil, want error")
	}
	req := httptest.NewRequest("GET", "/", nil)
	if _, err := mgr.Read(req); err == nil {
		t.Error("nil manager Read() error = nil, want error")
	}
}

func TestSessionManager_Clear(t *testing.T) {
	mgr := auth.NewSessionManager("test-session-secret-value-32bytes!", time.Hour, true)
	rec := httptest.NewRecorder()
	mgr.Clear(rec)
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected 1 cookie, got %d", len(cookies))
	}
	if cookies[0].MaxAge != -1 {
		t.Errorf("MaxAge = %d, want -1", cookies[0].MaxAge)
	}
	if !cookies[0].Secure {
		t.Error("expected Secure cookie when manager configured with secure=true")
	}
}

func TestSessionManager_Read_ErrorPaths(t *testing.T) {
	mgr := auth.NewSessionManager("test-session-secret-value-32bytes!", time.Hour, false)
	other := auth.NewSessionManager("a-completely-different-secret-32!!", time.Hour, false)

	t.Run("no cookie", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		if _, err := mgr.Read(req); err == nil {
			t.Error("expected error with no session cookie")
		}
	})

	t.Run("expired session", func(t *testing.T) {
		rec := httptest.NewRecorder()
		if err := mgr.Issue(rec, auth.Session{
			UserID:    "user-1",
			ExpiresAt: time.Now().UTC().Add(-time.Hour),
		}); err != nil {
			t.Fatalf("issue: %v", err)
		}
		req := httptest.NewRequest("GET", "/", nil)
		for _, c := range rec.Result().Cookies() {
			req.AddCookie(c)
		}
		if _, err := mgr.Read(req); err == nil {
			t.Error("expected error for expired session")
		}
	})

	t.Run("empty subject", func(t *testing.T) {
		rec := httptest.NewRecorder()
		if err := mgr.Issue(rec, auth.Session{
			ExpiresAt: time.Now().UTC().Add(time.Hour),
		}); err != nil {
			t.Fatalf("issue: %v", err)
		}
		req := httptest.NewRequest("GET", "/", nil)
		for _, c := range rec.Result().Cookies() {
			req.AddCookie(c)
		}
		if _, err := mgr.Read(req); err == nil {
			t.Error("expected error for empty session subject")
		}
	})

	t.Run("wrong signature", func(t *testing.T) {
		rec := httptest.NewRecorder()
		if err := other.Issue(rec, auth.Session{
			UserID:    "user-1",
			ExpiresAt: time.Now().UTC().Add(time.Hour),
		}); err != nil {
			t.Fatalf("issue: %v", err)
		}
		req := httptest.NewRequest("GET", "/", nil)
		for _, c := range rec.Result().Cookies() {
			req.AddCookie(c)
		}
		if _, err := mgr.Read(req); err == nil {
			t.Error("expected signature verification error when secrets differ")
		}
	})

	t.Run("malformed token", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		req.AddCookie(&http.Cookie{Name: "pgqn_session", Value: "not-a-valid-token"})
		if _, err := mgr.Read(req); err == nil {
			t.Error("expected error for malformed token")
		}
	})
}

func TestSessionManager_RefreshTokenSealedRoundTrip(t *testing.T) {
	mgr := auth.NewSessionManager("test-session-secret-value-32bytes!", time.Hour, false)
	rec := httptest.NewRecorder()
	if err := mgr.Issue(rec, auth.Session{
		UserID:       "user-1",
		ExpiresAt:    time.Now().UTC().Add(time.Hour),
		RefreshToken: "super-secret-refresh-token",
	}); err != nil {
		t.Fatalf("issue: %v", err)
	}
	req := httptest.NewRequest("GET", "/", nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	s, err := mgr.Read(req)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if s.RefreshToken != "super-secret-refresh-token" {
		t.Errorf("RefreshToken = %q, want round-tripped plaintext", s.RefreshToken)
	}
}

func TestSessionManager_RefreshHandler(t *testing.T) {
	mgr := auth.NewSessionManager("test-session-secret-value-32bytes!", time.Hour, false)

	t.Run("disabled", func(t *testing.T) {
		var disabled *auth.SessionManager
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/", nil)
		disabled.RefreshHandler(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rec.Code)
		}
	})

	t.Run("wrong method", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/", nil)
		mgr.RefreshHandler(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("status = %d, want 405", rec.Code)
		}
	})

	t.Run("no session", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/", nil)
		mgr.RefreshHandler(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", rec.Code)
		}
	})

	t.Run("valid session refreshed", func(t *testing.T) {
		issueRec := httptest.NewRecorder()
		if err := mgr.Issue(issueRec, auth.Session{
			UserID:    "user-1",
			ExpiresAt: time.Now().UTC().Add(time.Hour),
		}); err != nil {
			t.Fatalf("issue: %v", err)
		}
		req := httptest.NewRequest("POST", "/", nil)
		for _, c := range issueRec.Result().Cookies() {
			req.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		mgr.RefreshHandler(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
		}
	})
}

func TestSessionManager_StatusHandler(t *testing.T) {
	mgr := auth.NewSessionManager("test-session-secret-value-32bytes!", time.Hour, false)

	t.Run("disabled", func(t *testing.T) {
		var disabled *auth.SessionManager
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/", nil)
		disabled.StatusHandler(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rec.Code)
		}
	})

	t.Run("no session", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/", nil)
		mgr.StatusHandler(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", rec.Code)
		}
	})

	t.Run("authenticated", func(t *testing.T) {
		issueRec := httptest.NewRecorder()
		if err := mgr.Issue(issueRec, auth.Session{
			UserID:    "user-1",
			OrgID:     "org-1",
			Role:      auth.RoleAnalyst,
			ExpiresAt: time.Now().UTC().Add(time.Hour),
		}); err != nil {
			t.Fatalf("issue: %v", err)
		}
		req := httptest.NewRequest("GET", "/", nil)
		for _, c := range issueRec.Result().Cookies() {
			req.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		mgr.StatusHandler(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
		}
	})
}

func TestNewPKCEVerifier(t *testing.T) {
	verifier, challenge, err := auth.NewPKCEVerifier()
	if err != nil {
		t.Fatalf("NewPKCEVerifier() error = %v", err)
	}
	if verifier == "" || challenge == "" {
		t.Error("expected non-empty verifier and challenge")
	}
	if verifier == challenge {
		t.Error("verifier and challenge should differ (challenge is the S256 hash)")
	}
}

func TestLoadSessionManagerFromEnv(t *testing.T) {
	for _, k := range []string{"SECURITY_SESSION_SECRET", "SECURITY_SESSION_TTL", "APP_ENV", "SECURITY_STRICT"} {
		old, had := os.LookupEnv(k)
		defer func(k, old string, had bool) {
			if had {
				os.Setenv(k, old)
			} else {
				os.Unsetenv(k)
			}
		}(k, old, had)
	}

	os.Unsetenv("SECURITY_SESSION_SECRET")
	if mgr := auth.LoadSessionManagerFromEnv(); mgr != nil {
		t.Error("expected nil manager when secret env is unset")
	}

	os.Setenv("SECURITY_SESSION_SECRET", "env-secret-value-32-bytes-long!!")
	os.Setenv("SECURITY_SESSION_TTL", "2h")
	os.Setenv("APP_ENV", "production")
	mgr := auth.LoadSessionManagerFromEnv()
	if mgr == nil {
		t.Fatal("expected non-nil manager")
	}
	if mgr.TTL() != 2*time.Hour {
		t.Errorf("TTL() = %v, want 2h", mgr.TTL())
	}
}
