package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"math"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
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

	orgDSN  OrgDSNLookup
	orgLazy map[string]*lazyReadOnlyPool // key: orgID + "\x00" + connectionID

	maxOrgPools      int
	globalMaxConns   int
	perPoolMaxConns  int
	staticPoolBudget int // app + static readonly configured max
	orgPoolEvictions int64
}

// OrgDSNLookup resolves optional per-organisation encrypted analytics DSNs.
type OrgDSNLookup interface {
	Resolve(ctx context.Context, orgID, connectionID string) (auth.OrgConnectionResolution, error)
}

// SetOrgDSNLookup wires Phase 2 per-org DSN resolution. Nil disables org-owned pools.
func (p *Pools) SetOrgDSNLookup(lookup OrgDSNLookup) {
	if p == nil {
		return
	}
	p.readonlyMu.Lock()
	defer p.readonlyMu.Unlock()
	p.orgDSN = lookup
	if p.orgLazy == nil {
		p.orgLazy = make(map[string]*lazyReadOnlyPool)
	}
}

type readonlySpec struct {
	conn config.DataConnectionConfig
	opts poolOptions
}

type lazyReadOnlyPool struct {
	pool          *pgxpool.Pool
	lastUsed      time.Time
	policyVersion int64
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
		schemas := conn.AllowedSchemas
		if len(schemas) == 0 {
			schemas = cfg.AllowedSchemas
		}
		opts.searchPath = append([]string(nil), schemas...)
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
	maxOrgPools, staticBudget := maxOrgPoolsFromConfig(cfg)
	p := &Pools{
		ReadOnlyPools:       make(map[string]*pgxpool.Pool),
		DefaultConnectionID: defaultID,
		App:                 appPool,
		readonlySpecs:       readonlySpecs,
		readonlyLazy:        make(map[string]*lazyReadOnlyPool),
		orgLazy:             make(map[string]*lazyReadOnlyPool),
		poolDefaults:        poolDefaults,
		idleEvict:           defaultReadonlyIdleEvict,
		stopEvict:           stopEvict,
		maxOrgPools:         maxOrgPools,
		globalMaxConns:      cfg.GlobalMaxConns,
		perPoolMaxConns:     cfg.MaxConnections,
		staticPoolBudget:    staticBudget,
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
	// Reserve headroom for at least one tenant pool when org DSNs are in use.
	// Static budget is app + configured readonly sources; tenant pools consume the remainder at runtime.
	total := cfg.MaxConnections * (sourceCount + 1)
	if total > cfg.GlobalMaxConns {
		return fmt.Errorf("database global connection budget exceeded: configured maximum %d across %d pools, budget %d", total, sourceCount+1, cfg.GlobalMaxConns)
	}
	return nil
}

func maxOrgPoolsFromConfig(cfg config.DatabaseConfig) (maxPools, staticBudget int) {
	sourceCount := len(cfg.Connections)
	if sourceCount == 0 {
		sourceCount = 1
	}
	perPool := cfg.MaxConnections
	if perPool <= 0 {
		perPool = 5
	}
	staticBudget = perPool * (sourceCount + 1)
	if cfg.GlobalMaxConns <= 0 {
		// Soft default: allow a bounded number of tenant pools.
		return 32, staticBudget
	}
	remaining := cfg.GlobalMaxConns - staticBudget
	if remaining < perPool {
		return 0, staticBudget
	}
	return remaining / perPool, staticBudget
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
	for id, entry := range p.orgLazy {
		if entry != nil && entry.pool != nil {
			entry.pool.Close()
		}
		delete(p.orgLazy, id)
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

// HealthReport returns readiness for the app pool, configured read-only pools,
// and any initialized per-organisation tenant pools.
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
	for key, entry := range p.orgLazy {
		if entry == nil || entry.pool == nil {
			continue
		}
		name := tenantPoolMetricName(key)
		st := poolHealth(ctx, name, "tenant_readonly", entry.pool, true, true)
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
// Tenant pools use a bounded label (hashed org id) to avoid unbounded Prometheus cardinality.
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
	for key, entry := range p.orgLazy {
		if entry != nil && entry.pool != nil {
			out = append(out, NamedPool{Name: tenantPoolMetricName(key), Role: "tenant_readonly", Pool: entry.pool})
		}
	}
	p.readonlyMu.Unlock()
	return out
}

func tenantPoolMetricName(orgPoolKey string) string {
	orgID, connID, ok := strings.Cut(orgPoolKey, "\x00")
	if !ok {
		return "tenant:unknown"
	}
	sum := sha256.Sum256([]byte(orgID))
	return "tenant:" + hex.EncodeToString(sum[:4]) + ":" + connID
}

// ReadOnly returns the read-only pool for connectionID, lazily opening it when needed.
// When ctx carries an organisation with a Phase 2 encrypted DSN for connectionID,
// that org-owned pool is used instead of the shared catalog pool.
func (p *Pools) ReadOnly(ctx context.Context, connectionID string) *pgxpool.Pool {
	if p == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if connectionID == "" {
		connectionID = p.DefaultConnectionID
	}
	if orgID := auth.OrgIDFromContext(ctx); orgID != "" {
		pool, err := p.ensureOrgReadOnlyPool(ctx, orgID, connectionID)
		if err != nil {
			log.Printf("org readonly pool unavailable (org=%s conn=%s): %v", orgID, connectionID, err)
			return nil
		}
		if pool != nil {
			return pool
		}
	}
	pool, err := p.ensureReadOnlyPool(ctx, connectionID)
	if err != nil {
		log.Printf("readonly pool unavailable (conn=%s): %v", connectionID, err)
		return nil
	}
	return pool
}

// AllowedSchemas returns the effective schemas for the selected connection in the
// current request context, preferring per-organisation overrides when present.
// A non-nil empty slice means the tenant override is authoritative and denies all schemas.
// nil means no tenant override (use catalog schemas).
func (p *Pools) AllowedSchemas(ctx context.Context, connectionID string) []string {
	if p == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if connectionID == "" {
		connectionID = p.DefaultConnectionID
	}
	if orgID := auth.OrgIDFromContext(ctx); orgID != "" && p.orgDSN != nil {
		res, err := p.orgDSN.Resolve(ctx, orgID, connectionID)
		if err == nil && res.Mode != auth.OrgConnectionNoOverride {
			return append([]string(nil), res.Schemas...)
		}
	}
	if spec, ok := p.readonlySpecs[connectionID]; ok {
		return append([]string(nil), spec.opts.searchPath...)
	}
	return nil
}

// TenantConnectionPolicy is the authoritative per-request tenant analytics policy.
type TenantConnectionPolicy struct {
	OrganizationID string
	ConnectionID   string
	Mode           auth.OrgConnectionMode
	Pool           *pgxpool.Pool
	AllowedSchemas []string
	ReadOnlyUser   string
	QueryTimeout   time.Duration
	LockTimeout    time.Duration
	IdleTxTimeout  time.Duration
	PolicyVersion  int64
}

// ResolveTenantConnectionPolicy returns the pool, schemas, and timeouts that every
// query/catalog/stats path must use for the current organisation and connection.
func (p *Pools) ResolveTenantConnectionPolicy(ctx context.Context, connectionID string) (*TenantConnectionPolicy, error) {
	if p == nil {
		return nil, fmt.Errorf("pools are not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if connectionID == "" {
		connectionID = p.DefaultConnectionID
	}
	policy := &TenantConnectionPolicy{
		OrganizationID: auth.OrgIDFromContext(ctx),
		ConnectionID:   connectionID,
		Mode:           auth.OrgConnectionNoOverride,
	}
	if spec, ok := p.readonlySpecs[connectionID]; ok {
		policy.AllowedSchemas = append([]string(nil), spec.opts.searchPath...)
		policy.ReadOnlyUser = spec.conn.ReadOnlyUser
		policy.QueryTimeout = spec.opts.statementTimeout
		policy.LockTimeout = spec.opts.lockTimeout
		policy.IdleTxTimeout = spec.opts.idleTxTimeout
	}

	if policy.OrganizationID != "" && p.orgDSN != nil {
		res, err := p.orgDSN.Resolve(ctx, policy.OrganizationID, connectionID)
		if err != nil {
			return nil, err
		}
		policy.Mode = res.Mode
		policy.PolicyVersion = res.Version
		if res.FailClosed() {
			return nil, res.Error()
		}
		if res.Mode == auth.OrgConnectionDedicated {
			policy.AllowedSchemas = append([]string(nil), res.Schemas...)
			if user := dsnUser(res.DSN); user != "" {
				policy.ReadOnlyUser = user
			}
		}
	}

	pool := p.ReadOnly(ctx, connectionID)
	if pool == nil {
		return nil, fmt.Errorf("analytical pool unavailable for connection %q", connectionID)
	}
	policy.Pool = pool
	return policy, nil
}

// InvalidateOrgReadOnlyPool closes and removes the cached tenant pool for one
// organisation/connection pair so secret rotation and unassignment take effect immediately.
func (p *Pools) InvalidateOrgReadOnlyPool(orgID, connectionID string) {
	if p == nil {
		return
	}
	key := orgPoolKey(strings.TrimSpace(orgID), strings.TrimSpace(connectionID))
	p.readonlyMu.Lock()
	defer p.readonlyMu.Unlock()
	if entry := p.orgLazy[key]; entry != nil && entry.pool != nil {
		entry.pool.Close()
	}
	delete(p.orgLazy, key)
}

func orgPoolKey(orgID, connectionID string) string {
	return orgID + "\x00" + connectionID
}

func (p *Pools) ensureOrgReadOnlyPool(ctx context.Context, orgID, connectionID string) (*pgxpool.Pool, error) {
	if p.orgDSN == nil {
		return nil, nil
	}
	key := orgPoolKey(orgID, connectionID)
	lookup := p.orgDSN

	res, err := lookup.Resolve(ctx, orgID, connectionID)
	if err != nil {
		return nil, err
	}
	if res.FailClosed() {
		return nil, res.Error()
	}
	if res.Mode != auth.OrgConnectionDedicated || strings.TrimSpace(res.DSN) == "" {
		return nil, nil
	}

	p.readonlyMu.Lock()
	entry := p.orgLazy[key]
	if entry != nil && entry.pool != nil {
		if entry.policyVersion == res.Version {
			entry.lastUsed = time.Now()
			pool := entry.pool
			p.readonlyMu.Unlock()
			return pool, nil
		}
		// Policy changed (rotation / schema update): drop stale pool under lock.
		entry.pool.Close()
		delete(p.orgLazy, key)
	}
	p.readonlyMu.Unlock()

	// Inherit timeouts from the static connection; only override search_path with tenant schemas.
	opts := p.poolDefaults
	if spec, has := p.readonlySpecs[connectionID]; has {
		opts = spec.opts
	}
	if len(res.Schemas) > 0 {
		opts.searchPath = append([]string(nil), res.Schemas...)
	}

	verifyOpts := TenantDSNVerifyOptions{
		RequireTLS:     config.StrictMode(),
		AllowedSchemas: res.Schemas,
	}
	if err := VerifyTenantDSNWithOptions(ctx, res.DSN, verifyOpts); err != nil {
		return nil, fmt.Errorf("tenant DSN security verification failed: %w", err)
	}

	pool, err := newPoolWithRetries(ctx, res.DSN, opts)
	if err != nil {
		return nil, fmt.Errorf("%w (org %s conn %s): %v", errors.ErrReadOnlyPoolFailed, orgID, connectionID, err)
	}

	p.readonlyMu.Lock()
	defer p.readonlyMu.Unlock()
	if existing := p.orgLazy[key]; existing != nil && existing.pool != nil && existing.policyVersion == res.Version {
		pool.Close()
		existing.lastUsed = time.Now()
		return existing.pool, nil
	}
	if existing := p.orgLazy[key]; existing != nil && existing.pool != nil {
		existing.pool.Close()
		delete(p.orgLazy, key)
	}
	if err := p.reserveOrgPoolLocked(); err != nil {
		pool.Close()
		return nil, err
	}
	p.orgLazy[key] = &lazyReadOnlyPool{pool: pool, lastUsed: time.Now(), policyVersion: res.Version}
	return pool, nil
}

// reserveOrgPoolLocked evicts LRU tenant pools until under maxOrgPools. Caller must hold readonlyMu.
func (p *Pools) reserveOrgPoolLocked() error {
	if p.maxOrgPools <= 0 {
		return fmt.Errorf("tenant pool budget exhausted: no capacity reserved for organisation pools")
	}
	for len(p.orgLazy) >= p.maxOrgPools {
		var oldestKey string
		var oldestTime time.Time
		first := true
		for key, entry := range p.orgLazy {
			if entry == nil {
				delete(p.orgLazy, key)
				continue
			}
			if first || entry.lastUsed.Before(oldestTime) {
				first = false
				oldestKey = key
				oldestTime = entry.lastUsed
			}
		}
		if oldestKey == "" {
			break
		}
		if entry := p.orgLazy[oldestKey]; entry != nil && entry.pool != nil {
			entry.pool.Close()
		}
		delete(p.orgLazy, oldestKey)
		atomic.AddInt64(&p.orgPoolEvictions, 1)
	}
	if len(p.orgLazy) >= p.maxOrgPools {
		return fmt.Errorf("tenant pool budget exhausted: max %d organisation pools", p.maxOrgPools)
	}
	return nil
}

// OrgPoolEvictions returns how many tenant pools were LRU-evicted to stay within budget.
func (p *Pools) OrgPoolEvictions() int64 {
	if p == nil {
		return 0
	}
	return atomic.LoadInt64(&p.orgPoolEvictions)
}

func dsnUser(dsn string) string {
	dsn = strings.TrimSpace(dsn)
	if u, err := url.Parse(dsn); err == nil && u.User != nil {
		if name := u.User.Username(); name != "" {
			return name
		}
	}
	for _, field := range strings.Fields(dsn) {
		key, val, ok := strings.Cut(field, "=")
		if ok && strings.EqualFold(strings.TrimSpace(key), "user") {
			return strings.TrimSpace(val)
		}
	}
	return ""
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
	for id, entry := range p.orgLazy {
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
		delete(p.orgLazy, id)
	}
}
