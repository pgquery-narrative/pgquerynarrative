package ratelimit

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresLimiter stores token buckets in app.rate_limit_buckets.
type PostgresLimiter struct {
	pool  *pgxpool.Pool
	rpm   int
	burst int
}

// NewPostgresLimiter creates a PostgreSQL-backed limiter for multi-instance deployments.
func NewPostgresLimiter(pool *pgxpool.Pool, rpm, burst int) *PostgresLimiter {
	if burst <= 0 {
		burst = rpm * 2
	}
	return &PostgresLimiter{pool: pool, rpm: rpm, burst: burst}
}

// Allow reports whether the request for key is allowed using a DB-backed token bucket.
func (l *PostgresLimiter) Allow(key string) bool {
	if l == nil || l.pool == nil || l.rpm <= 0 || key == "" {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	tx, err := l.pool.Begin(ctx)
	if err != nil {
		return true
	}
	defer tx.Rollback(ctx)

	var tokens float64
	var lastRefill time.Time
	err = tx.QueryRow(ctx, `
		SELECT tokens, last_refill
		FROM app.rate_limit_buckets
		WHERE bucket_key = $1
		FOR UPDATE
	`, key).Scan(&tokens, &lastRefill)
	if errors.Is(err, pgx.ErrNoRows) {
		tokens = float64(l.burst)
		lastRefill = time.Now()
	} else if err != nil {
		return true
	}

	elapsed := time.Since(lastRefill).Minutes()
	tokens += float64(l.rpm) * elapsed
	if tokens > float64(l.burst) {
		tokens = float64(l.burst)
	}
	if tokens < 1 {
		return false
	}
	tokens -= 1
	now := time.Now()
	_, err = tx.Exec(ctx, `
		INSERT INTO app.rate_limit_buckets (bucket_key, tokens, last_refill)
		VALUES ($1, $2, $3)
		ON CONFLICT (bucket_key) DO UPDATE
		SET tokens = EXCLUDED.tokens, last_refill = EXCLUDED.last_refill
	`, key, tokens, now)
	if err != nil {
		return true
	}
	_ = tx.Commit(ctx)
	return true
}

// NewLimiterFromConfig returns a rate limiter. When distributed is true and pool is non-nil,
// uses PostgreSQL-backed buckets safe for multi-replica deployments.
func NewLimiterFromConfig(pool *pgxpool.Pool, rpm, burst int, distributed bool) AllowFunc {
	if rpm <= 0 {
		return nil
	}
	if distributed && pool != nil {
		return NewPostgresLimiter(pool, rpm, burst)
	}
	return NewLimiter(rpm, burst)
}

// AllowFunc is implemented by in-memory and distributed limiters.
type AllowFunc interface {
	Allow(key string) bool
}
