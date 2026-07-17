package llm

import (
	"fmt"
	"sync"
	"time"
)

const (
	defaultCircuitThreshold = 5
	defaultCircuitCooldown  = 30 * time.Second
)

// CircuitBreaker opens after consecutive provider failures to avoid retry storms.
type CircuitBreaker struct {
	mu        sync.Mutex
	failures  int
	threshold int
	cooldown  time.Duration
	openUntil time.Time
}

// NewCircuitBreaker returns a breaker with production defaults.
func NewCircuitBreaker() *CircuitBreaker {
	return &CircuitBreaker{
		threshold: defaultCircuitThreshold,
		cooldown:  defaultCircuitCooldown,
	}
}

// Allow returns an error when the breaker is open.
func (c *CircuitBreaker) Allow() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Now().Before(c.openUntil) {
		return fmt.Errorf("LLM provider circuit open; retry after %s", c.openUntil.UTC().Format(time.RFC3339))
	}
	return nil
}

// RecordSuccess resets consecutive failure tracking.
func (c *CircuitBreaker) RecordSuccess() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failures = 0
	c.openUntil = time.Time{}
}

// RecordFailure increments failures and opens the circuit when threshold is reached.
func (c *CircuitBreaker) RecordFailure() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failures++
	if c.failures >= c.threshold {
		c.openUntil = time.Now().Add(c.cooldown)
		c.failures = 0
	}
}

// defaultBreaker is shared across LLM invocations within the process.
var defaultBreaker = NewCircuitBreaker()
