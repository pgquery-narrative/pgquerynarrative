package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// StagingConfig holds OIDC settings used for corporate IdP staging validation.
type StagingConfig struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	Audience     string
	JWKSURL      string
}

// StagingReport summarizes automated OIDC readiness checks against a corporate IdP.
type StagingReport struct {
	CheckedAt          time.Time `json:"checked_at"`
	Issuer             string    `json:"issuer"`
	AuthorizeEndpoint  string    `json:"authorize_endpoint"`
	TokenEndpoint      string    `json:"token_endpoint"`
	JWKSURL            string    `json:"jwks_url"`
	DiscoveryOK        bool      `json:"discovery_ok"`
	JWKSOK             bool      `json:"jwks_ok"`
	ClientConfigured   bool      `json:"client_configured"`
	RedirectConfigured bool      `json:"redirect_configured"`
	SessionReady       bool      `json:"session_ready"`
	OK                 bool      `json:"ok"`
	Issues             []string  `json:"issues,omitempty"`
}

// ValidateStaging performs automated checks against a corporate OIDC IdP configuration.
// It validates discovery, JWKS availability, and required client/session settings.
func ValidateStaging(ctx context.Context, cfg StagingConfig, sessionSecret string) StagingReport {
	report := StagingReport{
		CheckedAt: time.Now().UTC(),
		Issuer:    strings.TrimSpace(cfg.Issuer),
	}
	if report.Issuer == "" {
		report.Issues = append(report.Issues, "SECURITY_OIDC_ISSUER is required")
		return report
	}

	client := &http.Client{Timeout: 15 * time.Second}
	authURL, tokenURL, err := OIDCEndpoints(ctx, report.Issuer, client)
	if err != nil {
		report.Issues = append(report.Issues, "discovery failed: "+err.Error())
	} else if authURL == "" || tokenURL == "" {
		report.Issues = append(report.Issues, "discovery returned empty authorize or token endpoint")
	} else {
		report.DiscoveryOK = true
		report.AuthorizeEndpoint = authURL
		report.TokenEndpoint = tokenURL
	}

	jwksURL := strings.TrimSpace(cfg.JWKSURL)
	if jwksURL == "" {
		jwksURL = strings.TrimRight(report.Issuer, "/") + "/.well-known/jwks.json"
	}
	report.JWKSURL = jwksURL
	validator := NewOIDCValidator(OIDCConfig{
		Issuer:   report.Issuer,
		Audience: cfg.Audience,
		JWKSURL:  jwksURL,
	})
	if validator == nil {
		report.Issues = append(report.Issues, "OIDC validator could not be initialized")
	} else if err := validator.refreshJWKS(ctx); err != nil {
		report.Issues = append(report.Issues, "JWKS fetch failed: "+err.Error())
	} else {
		report.JWKSOK = true
	}

	if strings.TrimSpace(cfg.ClientID) == "" {
		report.Issues = append(report.Issues, "SECURITY_OIDC_CLIENT_ID is required for browser login")
	} else {
		report.ClientConfigured = true
	}
	if strings.TrimSpace(cfg.RedirectURL) == "" {
		report.Issues = append(report.Issues, "SECURITY_OIDC_REDIRECT_URL is required")
	} else {
		report.RedirectConfigured = true
	}
	if strings.TrimSpace(sessionSecret) == "" {
		report.Issues = append(report.Issues, "SECURITY_SESSION_SECRET is required for browser sessions")
	} else {
		report.SessionReady = true
	}
	if strings.TrimSpace(cfg.ClientSecret) == "" {
		// Public PKCE clients may omit a secret; not a hard failure.
	} else if !report.ClientConfigured {
		report.Issues = append(report.Issues, "SECURITY_OIDC_CLIENT_SECRET set but client id missing")
	}

	report.OK = report.DiscoveryOK && report.JWKSOK && report.ClientConfigured &&
		report.RedirectConfigured && report.SessionReady
	return report
}

// FormatStagingIssues returns a single-line summary for scripts.
func FormatStagingIssues(report StagingReport) string {
	if len(report.Issues) == 0 {
		return "ok"
	}
	return fmt.Sprintf("%d issue(s): %s", len(report.Issues), strings.Join(report.Issues, "; "))
}
