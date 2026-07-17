package auth_test

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
)

func TestSessionManager_IssueAndRead(t *testing.T) {
	mgr := auth.NewSessionManager("test-session-secret-value-32bytes!", time.Hour, false)
	if mgr == nil || !mgr.Enabled() {
		t.Fatal("session manager should be enabled")
	}
	rec := httptest.NewRecorder()
	if err := mgr.Issue(rec, auth.Session{
		UserID:    "user-1",
		OrgID:     auth.DefaultOrganizationID,
		Role:      auth.RoleAnalyst,
		ExpiresAt: time.Now().UTC().Add(time.Hour),
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
	if s.UserID != "user-1" {
		t.Fatalf("got user %q", s.UserID)
	}
}
