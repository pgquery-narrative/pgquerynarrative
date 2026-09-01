package config

import "testing"

func TestDemoMode(t *testing.T) {
	t.Setenv("APP_ENV", "")
	if DemoMode() {
		t.Fatal("empty APP_ENV must not enable demo mode")
	}
	t.Setenv("APP_ENV", "development")
	if DemoMode() {
		t.Fatal("development must not enable demo mode")
	}
	t.Setenv("APP_ENV", "production")
	if DemoMode() {
		t.Fatal("production must not enable demo mode")
	}
	t.Setenv("APP_ENV", "demo")
	if !DemoMode() {
		t.Fatal("APP_ENV=demo must enable demo mode")
	}
	t.Setenv("APP_ENV", "DEMO")
	if !DemoMode() {
		t.Fatal("APP_ENV=DEMO must enable demo mode (case-insensitive)")
	}
}
