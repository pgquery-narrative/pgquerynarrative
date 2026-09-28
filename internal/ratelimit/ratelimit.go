// Package ratelimit provides in-memory per-key rate limiting for the HTTP server.
// When SECURITY_RATE_LIMIT_RPM > 0, requests are limited per client key (IP or identity).
// Burst is supported: a token bucket refills at rpm tokens per minute, capped at burst.
package ratelimit

import (
	"sync"
	"time"
)

// Limiter limits the number of requests per key using a token bucket.
// Keys are typically client IP or authenticated identity.
type Limiter struct {
	mu         sync.Mutex
	tokens     map[string]float64
	lastRefill map[string]time.Time
	rpm        int
	burst      int
}

// NewLimiter returns a limiter that allows up to burst tokens per key, refilling at rpm per minute.
// When burst <= 0 it defaults to rpm * 2.
func NewLimiter(rpm int, burst int) *Limiter {
	if rpm <= 0 {
		return nil
	}
	if burst <= 0 {
		burst = rpm * 2
	}
	return &Limiter{
		tokens:     make(map[string]float64),
		lastRefill: make(map[string]time.Time),
		rpm:        rpm,
		burst:      burst,
	}
}

// Allow reports whether the request for key is allowed.
// It refills the token bucket for key (at rpm per minute, capped at burst), then consumes one token if available.
// Thread-safe.
func (l *Limiter) Allow(key string) bool {
	if l == nil || l.rpm <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	tokens := l.tokens[key]
	last := l.lastRefill[key]
	if last.IsZero() {
		// First use for this key: start with full bucket
		tokens = float64(l.burst)
		last = now
	} else {
		// Refill: tokens per minute elapsed
		elapsed := now.Sub(last).Minutes()
		tokens += float64(l.rpm) * elapsed
	}
	if tokens > float64(l.burst) {
		tokens = float64(l.burst)
	}
	l.lastRefill[key] = now
	if tokens >= 1 {
		l.tokens[key] = tokens - 1
		return true
	}
	l.tokens[key] = tokens
	return false
}
