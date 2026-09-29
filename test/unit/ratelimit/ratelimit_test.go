package ratelimit_test

import (
	"testing"

	"github.com/pgquerynarrative/pgquerynarrative/internal/ratelimit"
)

func TestNewLimiter(t *testing.T) {
	if got := ratelimit.NewLimiter(0, 0); got != nil {
		t.Errorf("NewLimiter(0, 0) = %v, want nil", got)
	}
	if got := ratelimit.NewLimiter(-1, 5); got != nil {
		t.Errorf("NewLimiter(-1, 5) = %v, want nil", got)
	}
	if got := ratelimit.NewLimiter(5, 0); got == nil {
		t.Error("NewLimiter(5, 0) = nil, want non-nil")
	}
}

func TestLimiter_Allow(t *testing.T) {
	// nil limiter allows all
	var nilLimiter *ratelimit.Limiter
	if !nilLimiter.Allow("any") {
		t.Error("nil limiter should allow")
	}

	// rpm=2, burst=2: token bucket starts with 2 tokens; first two allowed, third denied
	l := ratelimit.NewLimiter(2, 2)
	if l == nil {
		t.Fatal("NewLimiter(2, 2) returned nil")
	}
	key := "client-A"
	if !l.Allow(key) {
		t.Error("first request should be allowed")
	}
	if !l.Allow(key) {
		t.Error("second request should be allowed")
	}
	if l.Allow(key) {
		t.Error("third request should be denied (no tokens until refill)")
	}
	if l.Allow(key) {
		t.Error("fourth request should be denied")
	}
}

func TestLimiter_Allow_DifferentKeys(t *testing.T) {
	l := ratelimit.NewLimiter(1, 1)
	if l == nil {
		t.Fatal("NewLimiter(1, 1) returned nil")
	}
	if !l.Allow("client-1") {
		t.Error("client-1 first should be allowed")
	}
	if l.Allow("client-1") {
		t.Error("client-1 second should be denied")
	}
	if !l.Allow("client-2") {
		t.Error("client-2 first should be allowed (different key)")
	}
}
