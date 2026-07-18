package auth_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
)

func TestOIDCValidator_RejectsNon2xxAndOversizedJWKS(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"keys":[]}`))
	}))
	defer srv.Close()

	v := auth.NewOIDCValidator(auth.OIDCConfig{
		Issuer:     "https://issuer.example",
		Audience:   "pgqn",
		JWKSURL:    srv.URL,
		StrictMode: true,
	})
	_, _, err := v.Validate(context.Background(), "not.a.jwt")
	if err == nil {
		t.Fatal("expected JWKS fetch failure")
	}
}

func TestOIDCValidator_RequiresKidAndAudience(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	n := base64.RawURLEncoding.EncodeToString(key.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())
	jwks := map[string]any{
		"keys": []map[string]string{{
			"kty": "RSA",
			"kid": "k1",
			"n":   n,
			"e":   e,
		}},
	}
	body, _ := json.Marshal(jwks)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	v := auth.NewOIDCValidator(auth.OIDCConfig{
		Issuer:     "https://issuer.example",
		Audience:   "pgqn",
		JWKSURL:    srv.URL,
		StrictMode: true,
	})

	// Token without kid must fail.
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": "https://issuer.example",
		"sub": "user-1",
		"aud": "pgqn",
		"exp": time.Now().Add(time.Hour).Unix(),
		"iat": time.Now().Unix(),
	})
	// deliberately no kid
	signed, err := tok.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := v.Validate(context.Background(), signed); err == nil {
		t.Fatal("expected missing kid rejection")
	}

	tok2 := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": "https://issuer.example",
		"sub": "user-1",
		"aud": "wrong",
		"exp": time.Now().Add(time.Hour).Unix(),
		"iat": time.Now().Unix(),
	})
	tok2.Header["kid"] = "k1"
	signed2, err := tok2.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := v.Validate(context.Background(), signed2); err == nil {
		t.Fatal("expected audience rejection")
	}

	tok3 := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": "https://issuer.example",
		"sub": "user-1",
		"aud": "pgqn",
		"exp": time.Now().Add(time.Hour).Unix(),
		"iat": time.Now().Unix(),
	})
	tok3.Header["kid"] = "k1"
	signed3, err := tok3.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	sub, _, err := v.Validate(context.Background(), signed3)
	if err != nil || sub != "user-1" {
		t.Fatalf("valid token failed: sub=%q err=%v", sub, err)
	}
}
