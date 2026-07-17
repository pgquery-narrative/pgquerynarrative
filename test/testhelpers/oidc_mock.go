package testhelpers

import (
	"net/http/httptest"

	"github.com/pgquerynarrative/pgquerynarrative/app/auth/mockoidc"
)

// MockOIDCServer simulates a corporate OIDC IdP for integration tests.
type MockOIDCServer struct {
	Server   *httptest.Server
	Issuer   string
	Audience string
	ClientID string
	inner    *mockoidc.Server
}

// NewMockOIDCServer starts a local OIDC provider with discovery, JWKS, and token endpoints.
func NewMockOIDCServer(audience, clientID string) (*MockOIDCServer, error) {
	inner, err := mockoidc.New(audience, clientID)
	if err != nil {
		return nil, err
	}
	ts := httptest.NewServer(inner.Handler())
	inner.BindIssuer(ts.URL)
	return &MockOIDCServer{
		Server:   ts,
		Issuer:   inner.Issuer,
		Audience: audience,
		ClientID: clientID,
		inner:    inner,
	}, nil
}

// Close stops the mock IdP.
func (m *MockOIDCServer) Close() {
	if m != nil && m.Server != nil {
		m.Server.Close()
	}
}

// IssueBearerToken returns a signed JWT for API bearer auth tests.
func (m *MockOIDCServer) IssueBearerToken(sub string, roles []string) (string, error) {
	return m.inner.IssueBearerToken(sub, roles)
}

// AuthorizeURL builds the login redirect URL for manual debugging.
func (m *MockOIDCServer) AuthorizeURL(redirectURI, state, challenge string) string {
	return m.inner.AuthorizeURL(redirectURI, state, challenge)
}
