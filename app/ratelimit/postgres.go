package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/app/observability"
)

// FailureMode controls what happens to a request when the distributed limiter's
// storage (Postgres) is unreachable or errors.
type FailureMode string

const (
	// FailOpen allows the request through when storage fails (availability over strictness).
	// This is the default for backward compatibility.
	FailOpen FailureMode = "open"
	// FailClosed denies the request when storage fails (strictness over availability).
	FailClosed FailureMode = "closed"
	// FailLocalFallback denies/allows using a local in-memory limiter when storage fails,
	// trading cross-replica accuracy for continued (degraded) protection.
	FailLocalFallback FailureMode = "local_fallback"
)

// ParseFailureMode normalizes a configured failure mode string, defaulting to FailOpen
// for empty or unrecognized values.
func ParseFailureMode(s string) FailureMode {
	switch FailureMode(strings.ToLower(strings.TrimSpace(s))) {
	case FailClosed:
		return FailClosed
	case FailLocalFallback:
		return FailLocalFallback
	case FailOpen, "":
		return FailOpen
	default:
		return FailOpen
	}
}

// ValidFailureMode reports whether s is a recognized failure mode (or empty, which
// defaults to FailOpen). Used by configuration validation to fail fast on typos.
func ValidFailureMode(s string) bool {
	switch FailureMode(strings.ToLower(strings.TrimSpace(s))) {
	case FailOpen, FailClosed, FailLocalFallback, "":
		return true
	default:
		return false
	}
}

// pgQuerier is the subset of *pgxpool.Pool used by PostgresLimiter. Extracted as an
// interface so unit tests can exercise the concurrency/refill/failure-mode logic with
// a lightweight fake instead of a real PostgreSQL instance.
type pgQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// PostgresLimiter stores token buckets in app.rate_limit_buckets, safe for use from
// multiple application replicas concurrently.
type PostgresLimiter struct {
	pool        pgQuerier
	rpm         int
	burst       int
	failureMode FailureMode
	fallback    *Limiter
}

// NewPostgresLimiter creates a PostgreSQL-backed limiter for multi-instance deployments.
// mode controls behavior when the storage query itself fails (not when the bucket is simply
// out of tokens); see FailOpen, FailClosed, FailLocalFallback.
func NewPostgresLimiter(pool *pgxpool.Pool, rpm, burst int, mode FailureMode) *PostgresLimiter {
	if burst <= 0 {
		burst = rpm * 2
	}
	var q pgQuerier
	if pool != nil {
		q = pool
	}
	return newPostgresLimiter(q, rpm, burst, mode)
}

func newPostgresLimiter(pool pgQuerier, rpm, burst int, mode FailureMode) *PostgresLimiter {
	return &PostgresLimiter{
		pool:        pool,
		rpm:         rpm,
		burst:       burst,
		failureMode: ParseFailureMode(string(mode)),
		fallback:    NewLimiter(rpm, burst),
	}
}

// upsertBucketSQL atomically refills and consumes one token from a bucket in a single
// round trip. Using one INSERT ... ON CONFLICT ... WHERE ... RETURNING statement (rather
// than a separate SELECT ... FOR UPDATE followed by INSERT/UPDATE) means Postgres itself
// serializes concurrent first-time requests for the same key via the unique index: only one
// concurrent transaction performs the initial INSERT, and the rest are forced through the
// DO UPDATE branch against the now-existing row. This closes the race where N concurrent
// "first" requests for a brand new key could each independently decide the bucket starts
// full and all be allowed (a burst multiply bug).
//
// $1 = bucket_key, $2 = burst (float8), $3 = rpm (float8, tokens refilled per minute).
// On conflict, refill is computed from elapsed time since last_refill, capped at burst,
// then one token is consumed. The WHERE clause ensures the row is only updated (and the
// request allowed) when at least one token is available after refill; when unavailable,
// zero rows are affected/returned and the caller treats that as "denied".
const upsertBucketSQL = `
INSERT INTO app.rate_limit_buckets (bucket_key, tokens, last_refill)
VALUES ($1, $2::double precision - 1, now())
ON CONFLICT (bucket_key) DO UPDATE SET
	tokens = LEAST(
		$2::double precision,
		app.rate_limit_buckets.tokens + $3::double precision * EXTRACT(EPOCH FROM (now() - app.rate_limit_buckets.last_refill)) / 60.0
	) - 1,
	last_refill = now()
WHERE LEAST(
	$2::double precision,
	app.rate_limit_buckets.tokens + $3::double precision * EXTRACT(EPOCH FROM (now() - app.rate_limit_buckets.last_refill)) / 60.0
) >= 1
RETURNING tokens
`

// AllowCtx reports whether the request for key is allowed, performing the token-bucket
// refill and consumption atomically in Postgres. The returned error is non-nil only for
// storage failures (e.g. connection errors); a denied-but-healthy-storage result returns
// (false, nil). Callers that need failure-mode semantics should use Allow or AllowWithMode.
func (l *PostgresLimiter) AllowCtx(ctx context.Context, key string) (bool, error) {
	if l == nil || l.pool == nil || l.rpm <= 0 || key == "" {
		return true, nil
	}
	qctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var tokens float64
	err := l.pool.QueryRow(qctx, upsertBucketSQL, key, float64(l.burst), float64(l.rpm)).Scan(&tokens)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("ratelimit: storage query failed: %w", err)
	}
	return true, nil
}

// Allow implements AllowFunc using the limiter's configured failure mode when storage fails.
func (l *PostgresLimiter) Allow(key string) bool {
	if l == nil {
		return true
	}
	return l.AllowWithMode(context.Background(), key, l.failureMode)
}

// AllowWithMode is like Allow but lets the caller override the failure mode for this call
// (e.g. httpmw forces FailClosed for AI/report routes in strict production regardless of
// the limiter's globally configured mode).
func (l *PostgresLimiter) AllowWithMode(ctx context.Context, key string, mode FailureMode) bool {
	if l == nil || l.pool == nil || l.rpm <= 0 || key == "" {
		return true
	}
	allowed, err := l.AllowCtx(ctx, key)
	if err == nil {
		return allowed
	}
	observability.IncRateLimitStorageFailure()
	switch ParseFailureMode(string(mode)) {
	case FailClosed:
		return false
	case FailLocalFallback:
		return l.fallback.Allow(key)
	default:
		return true
	}
}

// CleanupInactive deletes buckets that have not been touched in olderThan, keeping the
// table from growing unbounded with abandoned keys (e.g. rotated IPs, closed sessions).
// Returns the number of rows removed.
func (l *PostgresLimiter) CleanupInactive(ctx context.Context, olderThan time.Duration) (int64, error) {
	if l == nil || l.pool == nil {
		return 0, nil
	}
	cutoff := time.Now().Add(-olderThan)
	tag, err := l.pool.Exec(ctx, `DELETE FROM app.rate_limit_buckets WHERE last_refill < $1`, cutoff)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// StartCleanupLoop runs CleanupInactive on interval until ctx is done. It is safe to call
// at most once per limiter; interval <= 0 disables the loop.
func (l *PostgresLimiter) StartCleanupLoop(ctx context.Context, interval, olderThan time.Duration) {
	if l == nil || l.pool == nil || interval <= 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := l.CleanupInactive(ctx, olderThan); err != nil {
					observability.IncRateLimitStorageFailure()
				}
			}
		}
	}()
}

// AllowErr is implemented by limiters that can report storage failures separately from
// "request denied" so callers can apply failure-mode policy.
type AllowErr interface {
	AllowCtx(ctx context.Context, key string) (bool, error)
}

// ModeAwareAllower is implemented by limiters whose failure-mode can be overridden per call
// (e.g. to force fail-closed for high-sensitivity routes regardless of global configuration).
type ModeAwareAllower interface {
	AllowWithMode(ctx context.Context, key string, mode FailureMode) bool
}

// NewLimiterFromConfig returns a rate limiter. When distributed is true and pool is non-nil,
// uses PostgreSQL-backed buckets safe for multi-replica deployments.
func NewLimiterFromConfig(pool *pgxpool.Pool, rpm, burst int, distributed bool, mode FailureMode) AllowFunc {
	if rpm <= 0 {
		return nil
	}
	if distributed && pool != nil {
		return NewPostgresLimiter(pool, rpm, burst, mode)
	}
	return NewLimiter(rpm, burst)
}

// AllowFunc is implemented by in-memory and distributed limiters.
type AllowFunc interface {
	Allow(key string) bool
}
