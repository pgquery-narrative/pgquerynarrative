package db

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/app/config"
	"github.com/pgquerynarrative/pgquerynarrative/app/errors"
)

// Pools holds the read-only and app database connection pools. Call Close when done.
type Pools struct {
	ReadOnlyPools       map[string]*pgxpool.Pool
	DefaultConnectionID string
	App                 *pgxpool.Pool
}

type poolOptions struct {
	maxConns        int32
	searchPath      []string
	maxConnIdleTime time.Duration
	connectTimeout  time.Duration
}

// NewPools creates both connection pools from the given database config. It retries
// on failure and pings to verify connectivity. Caller must call Pools.Close() when done.
func NewPools(ctx context.Context, cfg config.DatabaseConfig) (*Pools, error) {
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
	poolDefaults := poolOptions{
		maxConns:        maxConns32,
		maxConnIdleTime: 5 * time.Minute,
		connectTimeout:  10 * time.Second,
	}

	readOnlyPools := make(map[string]*pgxpool.Pool, len(connections))
	for _, conn := range connections {
		readOnlyURL := buildConnectionURL(
			conn.ReadOnlyUser,
			conn.ReadOnlyPassword,
			conn.Host,
			conn.Port,
			conn.Database,
			conn.SSLMode,
		)
		opts := poolDefaults
		opts.searchPath = append([]string(nil), conn.AllowedSchemas...)
		pool, err := newPoolWithRetries(ctx, readOnlyURL, opts)
		if err != nil {
			closePools(readOnlyPools)
			return nil, fmt.Errorf("%w (%s): %v", errors.ErrReadOnlyPoolFailed, conn.ID, err)
		}
		readOnlyPools[conn.ID] = pool
	}

	appPool, err := newPoolWithRetries(ctx, appURL, poolDefaults)
	if err != nil {
		closePools(readOnlyPools)
		return nil, fmt.Errorf("%w: %v", errors.ErrAppPoolFailed, err)
	}

	defaultID := cfg.DefaultID
	if defaultID == "" {
		defaultID = "default"
	}
	if _, ok := readOnlyPools[defaultID]; !ok {
		for id := range readOnlyPools {
			defaultID = id
			break
		}
	}

	return &Pools{
		ReadOnlyPools:       readOnlyPools,
		DefaultConnectionID: defaultID,
		App:                 appPool,
	}, nil
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
	cfg.MinConns = 2
	cfg.MaxConnLifetime = 30 * time.Minute
	if opts.maxConnIdleTime > 0 {
		cfg.MaxConnIdleTime = opts.maxConnIdleTime
	}
	if opts.connectTimeout > 0 {
		cfg.ConnConfig.ConnectTimeout = opts.connectTimeout
	}
	if len(opts.searchPath) > 0 {
		schemas := opts.searchPath
		cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
			parts := make([]string, len(schemas))
			for i, s := range schemas {
				parts[i] = pgx.Identifier{s}.Sanitize()
			}
			_, err := conn.Exec(ctx, "SET search_path TO "+strings.Join(parts, ", "))
			return err
		}
	}
	return pgxpool.NewWithConfig(ctx, cfg)
}

func maxConns(max int) int32 {
	if max < math.MaxInt32 {
		return int32(max)
	}
	return math.MaxInt32
}

func buildConnectionURL(user, password, host string, port int, database, sslMode string) string {
	return fmt.Sprintf(
		"postgres://%s:%s@%s:%d/%s?sslmode=%s",
		user,
		password,
		host,
		port,
		database,
		sslMode,
	)
}

func (p *Pools) Close() {
	if p == nil {
		return
	}
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
	for id, pool := range p.ReadOnlyPools {
		if err := pool.Ping(ctx); err != nil {
			return fmt.Errorf("%w (read-only %s): %v", errors.ErrPoolHealthCheckFailed, id, err)
		}
	}
	if err := p.App.Ping(ctx); err != nil {
		return fmt.Errorf("%w (app): %v", errors.ErrPoolHealthCheckFailed, err)
	}
	return nil
}

// ReadOnly returns the read-only pool for connectionID, falling back to default.
func (p *Pools) ReadOnly(connectionID string) *pgxpool.Pool {
	if p == nil {
		return nil
	}
	if pool, ok := p.ReadOnlyPools[connectionID]; ok {
		return pool
	}
	return p.ReadOnlyPools[p.DefaultConnectionID]
}

func closePools(pools map[string]*pgxpool.Pool) {
	for _, p := range pools {
		if p != nil {
			p.Close()
		}
	}
}
