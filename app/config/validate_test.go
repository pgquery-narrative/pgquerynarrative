package config

import (
	"os"
	"testing"
)

func TestValidate_AuthRequiresKey(t *testing.T) {
	cfg := Config{Security: SecurityConfig{AuthEnabled: true, APIKey: ""}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error when auth enabled without API key")
	}
}

func TestValidate_ShortAPIKey(t *testing.T) {
	cfg := Config{Security: SecurityConfig{AuthEnabled: true, APIKey: "short"}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for short API key")
	}
}

func TestValidate_StrictMode(t *testing.T) {
	save := os.Getenv("APP_ENV")
	defer os.Setenv("APP_ENV", save)
	os.Setenv("APP_ENV", "production")

	cfg := Load()
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected strict validation error with default dev config")
	}
}

func TestValidate_DevModePasses(t *testing.T) {
	save := os.Getenv("APP_ENV")
	defer os.Setenv("APP_ENV", save)
	os.Unsetenv("APP_ENV")
	os.Unsetenv("SECURITY_STRICT")

	cfg := Load()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("dev config should validate: %v", err)
	}
}
