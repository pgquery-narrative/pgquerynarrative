package ratelimit

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// fakeRow implements pgx.Row over a fixed value/error, for testing PostgresLimiter's SQL
// orchestration without a real PostgreSQL instance.
type fakeRow struct {
	tokens float64
	err    error
	noRows bool
}

func (f fakeRow) Scan(dest ...any) error {
	if f.err != nil {
		return f.err
	}
	if f.noRows {
		return pgx.ErrNoRows
	}
	if len(dest) != 1 {
		return errors.New("fakeRow: expected exactly one scan destination")
	}
	ptr, ok := dest[0].(*float64)
	if !ok {
		return errors.New("fakeRow: expected *float64 destination")
	}
	*ptr = f.tokens
	return nil
}

// fakeStore is a minimal, mutex-protected in-memory implementation of the upsertBucketSQL
// semantics (refill-then-consume-one, atomically per key), used to validate PostgresLimiter's
// Go-side orchestration: correct SQL argument plumbing, refill math, and denial when the
// simulated backend reports zero rows. It also lets tests simulate storage failures via
// forcedErr to exercise FailureMode handling deterministically.
type fakeStore struct {
	mu        sync.Mutex
	buckets   map[string]bucketState
	forcedErr error
	queryRowN atomic.Int64
}

type bucketState struct {
	tokens float64
	last   time.Time
}

func newFakeStore() *fakeStore {
	return &fakeStore{buckets: make(map[string]bucketState)}
}

func (f *fakeStore) QueryRow(_ context.Context, _ string, args ...any) pgx.Row {
	f.queryRowN.Add(1)
	if f.forcedErr != nil {
		return fakeRow{err: f.forcedErr}
	}
	key := args[0].(string)
	burst := args[1].(float64)
	rpm := args[2].(float64)

	f.mu.Lock()
	defer f.mu.Unlock()

	st, ok := f.buckets[key]
	now := time.Now()
	if !ok {
		newTokens := burst - 1
		f.buckets[key] = bucketState{tokens: newTokens, last: now}
		return fakeRow{tokens: newTokens}
	}
	elapsedMin := now.Sub(st.last).Minutes()
	refilled := st.tokens + rpm*elapsedMin
	if refilled > burst {
		refilled = burst
	}
	if refilled < 1 {
		return fakeRow{noRows: true}
	}
	newTokens := refilled - 1
	f.buckets[key] = bucketState{tokens: newTokens, last: now}
	return fakeRow{tokens: newTokens}
}

func (f *fakeStore) Exec(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func TestPostgresLimiter_ConcurrentFirstRequest(t *testing.T) {
	// A fresh key under N concurrent requests must allow exactly `burst` of them, even though
	// the backing store is only correctly atomic if PostgresLimiter issues a single
	// query/round-trip per Allow call (no separate SELECT-then-INSERT race window in Go code).
	const burst = 5
	const concurrency = 50
	store := newFakeStore()
	l := newPostgresLimiter(store, 60, burst, FailOpen)

	var allowed atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if l.Allow("fresh-key") {
				allowed.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := allowed.Load(); got != burst {
		t.Errorf("allowed = %d, want exactly burst = %d", got, burst)
	}
	if got := store.queryRowN.Load(); got != concurrency {
		t.Errorf("expected exactly one storage round trip per Allow call, got %d for %d calls", got, concurrency)
	}
}

func TestPostgresLimiter_AllowCtx_DeniedWhenExhausted(t *testing.T) {
	store := newFakeStore()
	l := newPostgresLimiter(store, 60, 2, FailOpen)

	ok1, err1 := l.AllowCtx(context.Background(), "k")
	ok2, err2 := l.AllowCtx(context.Background(), "k")
	ok3, err3 := l.AllowCtx(context.Background(), "k")
	if err1 != nil || err2 != nil || err3 != nil {
		t.Fatalf("unexpected storage errors: %v %v %v", err1, err2, err3)
	}
	if !ok1 || !ok2 {
		t.Errorf("first two requests should be allowed (burst=2): ok1=%v ok2=%v", ok1, ok2)
	}
	if ok3 {
		t.Errorf("third request should be denied (bucket exhausted)")
	}
}

func TestPostgresLimiter_FailureModes(t *testing.T) {
	tests := []struct {
		name string
		mode FailureMode
		want bool
	}{
		{"open allows on storage failure", FailOpen, true},
		{"closed denies on storage failure", FailClosed, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newFakeStore()
			store.forcedErr = errors.New("connection refused")
			l := newPostgresLimiter(store, 60, 5, tt.mode)
			if got := l.Allow("any-key"); got != tt.want {
				t.Errorf("Allow() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPostgresLimiter_FailureMode_LocalFallback(t *testing.T) {
	store := newFakeStore()
	store.forcedErr = errors.New("connection refused")
	// burst=2: the local in-memory fallback limiter should allow exactly 2 requests per key
	// even though the distributed store is down for the whole test.
	l := newPostgresLimiter(store, 60, 2, FailLocalFallback)

	if !l.Allow("k") {
		t.Error("first request should be allowed by local fallback")
	}
	if !l.Allow("k") {
		t.Error("second request should be allowed by local fallback")
	}
	if l.Allow("k") {
		t.Error("third request should be denied by local fallback (burst exhausted)")
	}
}

func TestPostgresLimiter_AllowWithMode_OverridesConfiguredMode(t *testing.T) {
	store := newFakeStore()
	store.forcedErr = errors.New("connection refused")
	// Limiter configured fail-open, but the caller (e.g. httpmw for an AI route in strict
	// production) overrides to fail-closed for this call.
	l := newPostgresLimiter(store, 60, 5, FailOpen)
	if l.AllowWithMode(context.Background(), "k", FailClosed) {
		t.Error("AllowWithMode(FailClosed) should deny despite limiter's configured FailOpen mode")
	}
	if !l.AllowWithMode(context.Background(), "k", FailOpen) {
		t.Error("AllowWithMode(FailOpen) should allow")
	}
}

func TestPostgresLimiter_NilAndZeroRPM(t *testing.T) {
	var nilLimiter *PostgresLimiter
	if !nilLimiter.Allow("x") {
		t.Error("nil limiter should allow")
	}
	store := newFakeStore()
	l := newPostgresLimiter(store, 0, 5, FailOpen)
	if !l.Allow("x") {
		t.Error("rpm<=0 limiter should allow (disabled)")
	}
}

func TestParseFailureMode(t *testing.T) {
	cases := map[string]FailureMode{
		"":               FailOpen,
		"open":           FailOpen,
		"OPEN":           FailOpen,
		"closed":         FailClosed,
		"local_fallback": FailLocalFallback,
		"bogus":          FailOpen,
	}
	for in, want := range cases {
		if got := ParseFailureMode(in); got != want {
			t.Errorf("ParseFailureMode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidFailureMode(t *testing.T) {
	for _, v := range []string{"", "open", "closed", "local_fallback"} {
		if !ValidFailureMode(v) {
			t.Errorf("ValidFailureMode(%q) = false, want true", v)
		}
	}
	if ValidFailureMode("bogus") {
		t.Error("ValidFailureMode(\"bogus\") = true, want false")
	}
}

func TestPostgresLimiter_CleanupInactive(t *testing.T) {
	// CleanupInactive/StartCleanupLoop with a nil pool must be no-ops, not panics.
	l := NewPostgresLimiter(nil, 60, 10, FailOpen)
	n, err := l.CleanupInactive(context.Background(), time.Hour)
	if err != nil || n != 0 {
		t.Errorf("CleanupInactive on limiter with nil pool = (%d, %v), want (0, nil)", n, err)
	}
	l.StartCleanupLoop(context.Background(), time.Hour, time.Hour) // must not panic
}
