// Package pqncli is the terminal side of the "pqn" PostgreSQL extension. It talks to the pqn_api
// functions over an ordinary PostgreSQL login: no server, no REST, no API key. The Go engines
// (plan findings, rewrites, plan comparison) run here, and the database enforces every limit.
package pqncli

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgquerynarrative/pgquerynarrative/app/queryrunner"
)

// TopRow is one statement from pqn_api.top.
type TopRow struct {
	QueryID int64   `json:"queryid"`
	Query   string  `json:"query"`
	Calls   int64   `json:"calls"`
	TotalMs float64 `json:"total_ms"`
	MeanMs  float64 `json:"mean_ms"`
	Rows    int64   `json:"rows"`
}

// DoctorRow is one line of pqn_api.verify_setup.
type DoctorRow struct {
	Level  string `json:"level"`
	Check  string `json:"check"`
	Detail string `json:"detail"`
	Fix    string `json:"fix"`
}

// RunResult is the outcome of pqn_api.run.
type RunResult struct {
	Columns   []string         `json:"columns"`
	Rows      []map[string]any `json:"rows"`
	Truncated bool             `json:"truncated"`
}

// Measurement is what pqn_api.measure_pair reports: the row fingerprint and the best time of
// each statement.
type Measurement struct {
	Equal      bool    `json:"equal"`
	BeforeRows int64   `json:"before_rows"`
	AfterRows  int64   `json:"after_rows"`
	BeforeMs   float64 `json:"before_ms"`
	AfterMs    float64 `json:"after_ms"`
	Speedup    float64 `json:"speedup"`
	Rounds     int     `json:"rounds"`
}

// Proof is the verdict pqn_api.prove records.
type Proof struct {
	Verdict    string
	Reason     string
	CostBefore float64
	CostAfter  float64
	Measured   *Measurement
}

// Investigation is one ledger row.
type Investigation struct {
	ID        int64     `json:"id"`
	Who       string    `json:"who"`
	Title     string    `json:"title"`
	SQL       string    `json:"sql"`
	CreatedAt time.Time `json:"created_at"`
	Evidence  int64     `json:"evidence"`
}

// EvidenceRow is one piece of recorded evidence.
type EvidenceRow struct {
	ID        int64           `json:"id"`
	Kind      string          `json:"kind"`
	Who       string          `json:"who"`
	CreatedAt time.Time       `json:"created_at"`
	Payload   json.RawMessage `json:"payload"`
}

// Backend is everything the commands need from the database. The real one is pgBackend. Tests
// use a fake, so the investigation flow is tested without PostgreSQL.
type Backend interface {
	// HasReplica reports whether reads go to a second server.
	HasReplica() bool
	Top(ctx context.Context, n int) ([]TopRow, error)
	Plan(ctx context.Context, sql string) (json.RawMessage, error)
	Analyze(ctx context.Context, plan json.RawMessage) (*queryrunner.PlanAnalysis, error)
	MeasurePair(ctx context.Context, before, after string) (*Measurement, error)
	Run(ctx context.Context, sql string, limit int) (*RunResult, error)
	RecordInvestigation(ctx context.Context, sql, title string, queryID *int64) (int64, error)
	RecordEvidence(ctx context.Context, id int64, kind string, payload any) error
	Prove(ctx context.Context, id int64, before, after, note string) (*Proof, error)
	Investigations(ctx context.Context, n int) ([]Investigation, error)
	Evidence(ctx context.Context, id int64) ([]EvidenceRow, error)
	Doctor(ctx context.Context) ([]DoctorRow, error)
	Close()
}

// pgBackend talks to PostgreSQL. Reads (plan, run, measure, catalog) go to the replica when there
// is one. Everything that writes, and the workload statistics, go to the primary.
type pgBackend struct {
	primary *pgxpool.Pool
	replica *pgxpool.Pool
}

// Connect opens the primary and, optionally, the replica. An empty DSN means the standard libpq
// environment (PGHOST, PGUSER, PGSERVICE, ~/.pgpass ...), so the tool stores no secret.
func Connect(ctx context.Context, primaryDSN, replicaDSN string) (Backend, error) {
	open := func(dsn string) (*pgxpool.Pool, error) {
		cfg, err := pgxpool.ParseConfig(dsn)
		if err != nil {
			return nil, err
		}
		cfg.MaxConns = 3
		if cfg.ConnConfig.RuntimeParams == nil {
			cfg.ConnConfig.RuntimeParams = map[string]string{}
		}
		if _, ok := cfg.ConnConfig.RuntimeParams["application_name"]; !ok {
			cfg.ConnConfig.RuntimeParams["application_name"] = "pqn"
		}
		p, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			return nil, err
		}
		if err := p.Ping(ctx); err != nil {
			p.Close()
			return nil, err
		}
		return p, nil
	}
	primary, err := open(primaryDSN)
	if err != nil {
		return nil, fmt.Errorf("connect to the primary: %w", err)
	}
	b := &pgBackend{primary: primary}
	if replicaDSN != "" {
		b.replica, err = open(replicaDSN)
		if err != nil {
			primary.Close()
			return nil, fmt.Errorf("connect to the replica: %w", err)
		}
	}
	return b, nil
}

func (b *pgBackend) reader() *pgxpool.Pool {
	if b.replica != nil {
		return b.replica
	}
	return b.primary
}

func (b *pgBackend) HasReplica() bool { return b.replica != nil }

func (b *pgBackend) Close() {
	b.primary.Close()
	if b.replica != nil {
		b.replica.Close()
	}
}

func (b *pgBackend) Top(ctx context.Context, n int) ([]TopRow, error) {
	rows, err := b.primary.Query(ctx,
		`SELECT COALESCE(queryid, 0), query, calls, total_exec_time, mean_exec_time, "rows" FROM pqn_api.top($1)`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TopRow
	for rows.Next() {
		var r TopRow
		if err := rows.Scan(&r.QueryID, &r.Query, &r.Calls, &r.TotalMs, &r.MeanMs, &r.Rows); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (b *pgBackend) Plan(ctx context.Context, sql string) (json.RawMessage, error) {
	var s string
	if err := b.reader().QueryRow(ctx, `SELECT pqn_api.plan($1)::text`, sql).Scan(&s); err != nil {
		return nil, err
	}
	return json.RawMessage(s), nil
}

func (b *pgBackend) Analyze(ctx context.Context, plan json.RawMessage) (*queryrunner.PlanAnalysis, error) {
	return queryrunner.AnalyzePlanJSON(ctx, b.reader(), plan)
}

type measurementJSON struct {
	Equal  bool `json:"equal"`
	Before struct {
		Rows int64   `json:"rows"`
		Ms   float64 `json:"ms"`
	} `json:"before"`
	After struct {
		Rows int64   `json:"rows"`
		Ms   float64 `json:"ms"`
	} `json:"after"`
	Speedup *float64 `json:"speedup"`
	Rounds  int      `json:"rounds"`
}

func (m measurementJSON) toMeasurement() *Measurement {
	out := &Measurement{Equal: m.Equal, BeforeRows: m.Before.Rows, AfterRows: m.After.Rows,
		BeforeMs: m.Before.Ms, AfterMs: m.After.Ms, Rounds: m.Rounds}
	if m.Speedup != nil {
		out.Speedup = *m.Speedup
	}
	return out
}

func (b *pgBackend) MeasurePair(ctx context.Context, before, after string) (*Measurement, error) {
	var s string
	if err := b.reader().QueryRow(ctx, `SELECT pqn_api.measure_pair($1, $2, 2)::text`, before, after).Scan(&s); err != nil {
		return nil, err
	}
	var m measurementJSON
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, err
	}
	return m.toMeasurement(), nil
}

func (b *pgBackend) Run(ctx context.Context, sql string, limit int) (*RunResult, error) {
	var s string
	if err := b.reader().QueryRow(ctx, `SELECT pqn_api.run($1, $2)::text`, sql, limit).Scan(&s); err != nil {
		return nil, err
	}
	var raw struct {
		Columns   []string         `json:"columns"`
		Rows      []map[string]any `json:"rows"`
		Truncated bool             `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return nil, err
	}
	return &RunResult{Columns: raw.Columns, Rows: raw.Rows, Truncated: raw.Truncated}, nil
}

func (b *pgBackend) RecordInvestigation(ctx context.Context, sql, title string, queryID *int64) (int64, error) {
	var id int64
	err := b.primary.QueryRow(ctx, `SELECT pqn_api.record_investigation($1, $2, NULLIF($3, ''))`, sql, queryID, title).Scan(&id)
	return id, err
}

func (b *pgBackend) RecordEvidence(ctx context.Context, id int64, kind string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var evID int64
	return b.primary.QueryRow(ctx, `SELECT pqn_api.record_evidence($1, $2, $3::jsonb)`, id, kind, string(raw)).Scan(&evID)
}

func (b *pgBackend) Prove(ctx context.Context, id int64, before, after, note string) (*Proof, error) {
	var s string
	if err := b.primary.QueryRow(ctx, `SELECT pqn_api.prove($1, $2, $3, NULLIF($4, ''))::text`, id, before, after, note).Scan(&s); err != nil {
		return nil, err
	}
	var raw struct {
		Verdict     string           `json:"verdict"`
		Reason      string           `json:"reason"`
		CostBefore  float64          `json:"cost_before"`
		CostAfter   float64          `json:"cost_after"`
		Measurement *measurementJSON `json:"measurement"`
	}
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return nil, err
	}
	p := &Proof{Verdict: raw.Verdict, Reason: raw.Reason, CostBefore: raw.CostBefore, CostAfter: raw.CostAfter}
	if raw.Measurement != nil {
		p.Measured = raw.Measurement.toMeasurement()
	}
	return p, nil
}

func (b *pgBackend) Investigations(ctx context.Context, n int) ([]Investigation, error) {
	rows, err := b.reader().Query(ctx,
		`SELECT id, who, COALESCE(title, ''), sql, created_at, evidence_count FROM pqn_api.investigations($1)`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Investigation
	for rows.Next() {
		var r Investigation
		if err := rows.Scan(&r.ID, &r.Who, &r.Title, &r.SQL, &r.CreatedAt, &r.Evidence); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (b *pgBackend) Evidence(ctx context.Context, id int64) ([]EvidenceRow, error) {
	rows, err := b.reader().Query(ctx, `SELECT id, kind, who, created_at, payload::text FROM pqn_api.evidence($1)`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EvidenceRow
	for rows.Next() {
		var r EvidenceRow
		var p string
		if err := rows.Scan(&r.ID, &r.Kind, &r.Who, &r.CreatedAt, &p); err != nil {
			return nil, err
		}
		r.Payload = json.RawMessage(p)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (b *pgBackend) Doctor(ctx context.Context) ([]DoctorRow, error) {
	rows, err := b.reader().Query(ctx, `SELECT level, check_name, detail, COALESCE(fix, '') FROM pqn_api.verify_setup()`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DoctorRow
	for rows.Next() {
		var r DoctorRow
		if err := rows.Scan(&r.Level, &r.Check, &r.Detail, &r.Fix); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
