package db

import (
	"context"
	"fmt"
	"math"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/app/config"
	"github.com/pgquerynarrative/pgquerynarrative/app/errors"
)

const defaultReadonlyIdleEvict = 15 * time.Minute

// Pools holds the read-only and app database connection pools. Call Close when done.
type Pools struct {
	ReadOnlyPools       map[string]*pgxpool.Pool
	DefaultConnectionID string
	App                 *pgxpool.Pool

	readonlySpecs map[string]readonlySpec
	readonlyLazy  map[string]*lazyReadOnlyPool
	readonlyMu    sync.Mutex
	poolDefaults  poolOptions
	idleEvict     time.Duration
	stopEvict     context.CancelFunc
}

type readonlySpec struct {
	conn config.DataConnectionConfig
	opts poolOptions
}

type lazyReadOnlyPool struct {
	pool     *pgxpool.Pool
	lastUsed time.Time
}

type poolOptions struct {
	maxConns         int32
	minConns         int32
	searchPath       []string
	statementTimeout time.Duration
	lockTimeout      time.Duration
	idleTxTimeout    time.Duration
	maxConnIdleTime  time.Duration
	connectTimeout   time.Duration
}

// NewPools creates the app pool and lazily initializes read-only pools on demand.
func NewPools(ctx context.Context, cfg config.DatabaseConfig) (*Pools, error) {
	if err := enforceGlobalConnectionBudget(cfg); err != nil {
		return nil, err
	}
	appURL := buildConnectionURL(
		cfg.User,
		cfg.Password,
		cfg.Host,
		cfg.Port,
		cfg.Database,
		cfg.SSLMode,
	)

	connections := cfg.Connections
	if len(connections) == 0 {
		connections = []config.DataConnectionConfig{{
			ID:               "default",
			Name:             "Default",
			Host:             cfg.Host,
			Port:             cfg.Port,
			Database:         cfg.Database,
			ReadOnlyUser:     cfg.ReadOnlyUser,
			ReadOnlyPassword: cfg.ReadOnlyPassword,
			SSLMode:          cfg.SSLMode,
			QueryTimeout:     cfg.QueryTimeout,
			AllowedSchemas:   append([]string(nil), cfg.AllowedSchemas...),
		}}
	}
	maxConns32 := maxConns(cfg.MaxConnections)
	minConns32 := minConns(cfg.MinConnections, maxConns32)
	poolDefaults := poolOptions{
		maxConns:        maxConns32,
		minConns:        minConns32,
		maxConnIdleTime: 5 * time.Minute,
		connectTimeout:  10 * time.Second,
	}

	readonlySpecs := make(map[string]readonlySpec, len(connections))
	for _, conn := range connections {
		opts := poolDefaults
		opts.searchPath = append([]string(nil), conn.AllowedSchemas...)
		opts.statementTimeout = conn.QueryTimeout
		opts.lockTimeout = conn.LockTimeout
		opts.idleTxTimeout = conn.IdleTxTimeout
		readonlySpecs[conn.ID] = readonlySpec{conn: conn, opts: opts}
	}

	appPool, err := newPoolWithRetries(ctx, appURL, poolDefaults)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errors.ErrAppPoolFailed, err)
	}

	defaultID := cfg.DefaultID
	if defaultID == "" {
		defaultID = "default"
	}
	if _, ok := readonlySpecs[defaultID]; !ok {
		for id := range readonlySpecs {
			defaultID = id
			break
		}
	}

	evictCtx, stopEvict := context.WithCancel(context.Background())
	p := &Pools{
		ReadOnlyPools:       make(map[string]*pgxpool.Pool),
		DefaultConnectionID: defaultID,
		App:                 appPool,
		readonlySpecs:       readonlySpecs,
		readonlyLazy:        make(map[string]*lazyReadOnlyPool),
		poolDefaults:        poolDefaults,
		idleEvict:           defaultReadonlyIdleEvict,
		stopEvict:           stopEvict,
	}
	go p.evictIdleReadonlyPools(evictCtx)
	return p, nil
}

func enforceGlobalConnectionBudget(cfg config.DatabaseConfig) error {
	if cfg.GlobalMaxConns <= 0 || cfg.MaxConnections <= 0 {
		return nil
	}
	sourceCount := len(cfg.Connections)
	if sourceCount == 0 {
		sourceCount = 1
	}
	total := cfg.MaxConnections * (sourceCount + 1)
	if total > cfg.GlobalMaxConns {
		return fmt.Errorf("database global connection budget exceeded: configured maximum %d across %d pools, budget %d", total, sourceCount+1, cfg.GlobalMaxConns)
	}
	return nil
}

func newPoolWithRetries(ctx context.Context, connURL string, opts poolOptions) (*pgxpool.Pool, error) {
	var pool *pgxpool.Pool
	var err error
	maxRetries := 3
	retryDelay := 2 * time.Second
	for i := 0; i < maxRetries; i++ {
		pool, err = newPool(ctx, connURL, opts)
		if err == nil {
			if pingErr := pool.Ping(ctx); pingErr != nil {
				pool.Close()
				err = fmt.Errorf("ping failed: %w", pingErr)
			} else {
				break
			}
		}
		if i < maxRetries-1 {
			time.Sleep(retryDelay)
			retryDelay *= 2
		}
	}
	if err != nil {
		return nil, err
	}
	return pool, nil
}

func newPool(ctx context.Context, connURL string, opts poolOptions) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(connURL)
	if err != nil {
		return nil, err
	}
	if opts.maxConns > 0 {
		cfg.MaxConns = opts.maxConns
	}
	cfg.MinConns = opts.minConns
	cfg.MaxConnLifetime = 30 * time.Minute
	if opts.maxConnIdleTime > 0 {
		cfg.MaxConnIdleTime = opts.maxConnIdleTime
	}
	if opts.connectTimeout > 0 {
		cfg.ConnConfig.ConnectTimeout = opts.connectTimeout
	}
	if len(opts.searchPath) > 0 || opts.statementTimeout > 0 || opts.lockTimeout > 0 || opts.idleTxTimeout > 0 {
		schemas := opts.searchPath
		cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
			if len(schemas) > 0 {
				parts := make([]string, len(schemas))
				for i, s := range schemas {
					parts[i] = pgx.Identifier{s}.Sanitize()
				}
				if _, err := conn.Exec(ctx, "SET search_path TO "+strings.Join(parts, ", ")); err != nil {
					return err
				}
			}
			for name, timeout := range map[string]time.Duration{
				"statement_timeout":                   opts.statementTimeout,
				"lock_timeout":                        opts.lockTimeout,
				"idle_in_transaction_session_timeout": opts.idleTxTimeout,
			} {
				if timeout <= 0 {
					continue
				}
				if _, err := conn.Exec(ctx, "SET "+name+" = "+strconv.Quote(durationMillis(timeout))); err != nil {
					return err
				}
			}
			return nil
		}
	}
	return pgxpool.NewWithConfig(ctx, cfg)
}

func maxConns(max int) int32 {
	if max <= 0 {
		return 0
	}
	if max < math.MaxInt32 {
		return int32(max)
	}
	return math.MaxInt32
}

func minConns(min int, max int32) int32 {
	if min <= 0 {
		return 0
	}
	v := maxConns(min)
	if max > 0 && v > max {
		return max
	}
	return v
}

func buildConnectionURL(user, password, host string, port int, database, sslMode string) string {
	u := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(user, password),
		Host:   net.JoinHostPort(host, strconv.Itoa(port)),
		Path:   "/" + database,
	}
	q := u.Query()
	q.Set("sslmode", sslMode)
	u.RawQuery = q.Encode()
	return u.String()
}

func durationMillis(d time.Duration) string {
	return strconv.FormatInt(d.Milliseconds(), 10) + "ms"
}

func (p *Pools) Close() {
	if p == nil {
		return
	}
	if p.stopEvict != nil {
		p.stopEvict()
	}
	p.readonlyMu.Lock()
	for id, entry := range p.readonlyLazy {
		if entry != nil && entry.pool != nil {
			entry.pool.Close()
		}
		delete(p.readonlyLazy, id)
	}
	p.readonlyMu.Unlock()
	for _, pool := range p.ReadOnlyPools {
		if pool != nil {
			pool.Close()
		}
	}
	if p.App != nil {
		p.App.Close()
	}
}

func (p *Pools) Health(ctx context.Context) error {
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	if err := p.App.Ping(pingCtx); err != nil {
		cancel()
		return fmt.Errorf("%w (app): %v", errors.ErrPoolHealthCheckFailed, err)
	}
	cancel()
	if err := CheckMigrationVersion(ctx, p.App); err != nil {
		return err
	}
	return nil
}

// HealthStatus reports individual pool readiness without failing fast.
type HealthStatus struct {
	Name        string `json:"name"`
	Role        string `json:"role"`
	Ready       bool   `json:"ready"`
	Lazy        bool   `json:"lazy,omitempty"`
	Initialized bool   `json:"initialized,omitempty"`
	Error       string `json:"error,omitempty"`
}

// HealthReport returns readiness for the app pool and every configured read-only pool.
func (p *Pools) HealthReport(ctx context.Context) []HealthStatus {
	if p == nil {
		return []HealthStatus{{Name: "client", Role: "client", Ready: false, Error: "not initialized"}}
	}
	out := []HealthStatus{poolHealth(ctx, "app", "app", p.App, false, true)}
	if p.App != nil {
		if err := CheckMigrationVersion(ctx, p.App); err != nil {
			out[0].Ready = false
			out[0].Error = err.Error()
		}
	}
	p.readonlyMu.Lock()
	for id := range p.readonlySpecs {
		entry := p.readonlyLazy[id]
		if entry == nil || entry.pool == nil {
			out = append(out, HealthStatus{
				Name:        id,
				Role:        "readonly",
				Ready:       true,
				Lazy:        true,
				Initialized: false,
				Error:       "lazy: not connected",
			})
			continue
		}
		st := poolHealth(ctx, id, "readonly", entry.pool, true, true)
		out = append(out, st)
	}
	p.readonlyMu.Unlock()
	return out
}

func poolHealth(ctx context.Context, name, role string, pool *pgxpool.Pool, lazy, initialized bool) HealthStatus {
	st := HealthStatus{Name: name, Role: role, Ready: true, Lazy: lazy, Initialized: initialized}
	if pool == nil {
		st.Ready = false
		st.Error = "pool not configured"
		return st
	}
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		st.Ready = false
		st.Error = err.Error()
	}
	return st
}

// NamedPool identifies a pool for metrics.
type NamedPool struct {
	Name string
	Role string
	Pool *pgxpool.Pool
}

// NamedPools returns initialized pools with stable labels.
func (p *Pools) NamedPools() []NamedPool {
	if p == nil {
		return nil
	}
	out := []NamedPool{{Name: "app", Role: "app", Pool: p.App}}
	p.readonlyMu.Lock()
	for id, entry := range p.readonlyLazy {
		if entry != nil && entry.pool != nil {
			out = append(out, NamedPool{Name: id, Role: "readonly", Pool: entry.pool})
		}
	}
	p.readonlyMu.Unlock()
	return out
}

// ReadOnly returns the read-only pool for connectionID, lazily opening it when needed.
func (p *Pools) ReadOnly(connectionID string) *pgxpool.Pool {
	if p == nil {
		return nil
	}
	if connectionID == "" {
		connectionID = p.DefaultConnectionID
	}
	pool, err := p.ensureReadOnlyPool(context.Background(), connectionID)
	if err != nil {
		return nil
	}
	return pool
}

func (p *Pools) ensureReadOnlyPool(ctx context.Context, connectionID string) (*pgxpool.Pool, error) {
	spec, ok := p.readonlySpecs[connectionID]
	if !ok {
		// Blank IDs are normalized to the default by ReadOnly(); unknown non-empty IDs must not fall back.
		return nil, fmt.Errorf("%w: %q", errors.ErrConnectionNotFound, connectionID)
	}

	p.readonlyMu.Lock()
	entry := p.readonlyLazy[connectionID]
	if entry != nil && entry.pool != nil {
		entry.lastUsed = time.Now()
		pool := entry.pool
		p.readonlyMu.Unlock()
		return pool, nil
	}
	p.readonlyMu.Unlock()

	conn := spec.conn
	readOnlyURL := buildConnectionURL(
		conn.ReadOnlyUser,
		conn.ReadOnlyPassword,
		conn.Host,
		conn.Port,
		conn.Database,
		conn.SSLMode,
	)
	pool, err := newPoolWithRetries(ctx, readOnlyURL, spec.opts)
	if err != nil {
		return nil, fmt.Errorf("%w (%s): %v", errors.ErrReadOnlyPoolFailed, connectionID, err)
	}

	p.readonlyMu.Lock()
	defer p.readonlyMu.Unlock()
	if existing := p.readonlyLazy[connectionID]; existing != nil && existing.pool != nil {
		pool.Close()
		existing.lastUsed = time.Now()
		return existing.pool, nil
	}
	p.readonlyLazy[connectionID] = &lazyReadOnlyPool{pool: pool, lastUsed: time.Now()}
	p.ReadOnlyPools[connectionID] = pool
	return pool, nil
}

func (p *Pools) evictIdleReadonlyPools(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.evictOnce()
		}
	}
}

func (p *Pools) evictOnce() {
	if p == nil {
		return
	}
	cutoff := time.Now().Add(-p.idleEvict)
	p.readonlyMu.Lock()
	defer p.readonlyMu.Unlock()
	for id, entry := range p.readonlyLazy {
		if entry == nil || entry.pool == nil {
			continue
		}
		if entry.lastUsed.After(cutoff) {
			continue
		}
		if stat := entry.pool.Stat(); stat.AcquiredConns() > 0 || stat.TotalConns() > stat.IdleConns() {
			continue
		}
		entry.pool.Close()
		delete(p.readonlyLazy, id)
		delete(p.ReadOnlyPools, id)
	}
}
