package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

// MountEcho must route under the prefix and hand the handler echo's own
// ResponseWriter/Request (malformed JSON is rejected before the client is used).
func TestMountEchoRoutesUnderPrefix(t *testing.T) {
	e := echo.New()
	MountEcho(e, nil, "/api/")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/query/run", strings.NewReader("{")))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "unexpected EOF") {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/query/run", strings.NewReader("{")))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("route outside prefix: got %d", rec.Code)
	}
}
