package queryrunner

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	apperrors "github.com/pgquerynarrative/pgquerynarrative/internal/errors"
)

// ReplayContext is the PostgREST request context a replay applies inside each
// read-only transaction, so plans and rows follow the captured request as far
// as this connection's policy allows.
type ReplayContext struct {
	// Role is assumed with SET LOCAL ROLE. Callers set it only for a role an
	// operator allowlisted and the analytical role may assume.
	Role string
	// SearchPath is the captured search_path. Only schemas this connection
	// allows are applied, in captured order, so unqualified names still resolve
	// inside the allowlist.
	SearchPath []string
	// Settings are captured settings; only replayable ones are applied.
	Settings map[string]string
	// request.jwt.claims from the captured log is never kept or replayed, even
	// for an impersonated role: the capture is caller-supplied text with no
	// verification it is authentic, and a forged claims value would let any
	// principal who can create a PostgREST-imported investigation choose the
	// identity an RLS policy keyed on it (for example auth.uid()) evaluates
	// for. Role impersonation remains bounded because Postgres itself checks
	// the analytical role actually holds that GRANT; nothing here can forge
	// that check.
	Method, Path string
}

type replayContextKey struct{}

// WithReplayContext attaches rc to ctx for every query the runner executes.
func WithReplayContext(ctx context.Context, rc *ReplayContext) context.Context {
	return context.WithValue(ctx, replayContextKey{}, rc)
}

func replayContextFrom(ctx context.Context) *ReplayContext {
	rc, _ := ctx.Value(replayContextKey{}).(*ReplayContext)
	return rc
}

// ReplayableSetting reports whether a captured setting is applied in a replay.
// Timeouts stay the product's own, so a replay can measure a statement that
// timed out in production.
func ReplayableSetting(name string) bool {
	switch strings.ToLower(name) {
	case "statement_timeout":
		return false
	}
	return replaySettingRe.MatchString(name)
}

type replayStmt struct {
	sql  string
	args []any
}

// ReplaySearchPath returns the captured schemas this connection allows, in
// captured order.
func (r *Runner) ReplaySearchPath(ctx context.Context, captured []string) []string {
	v := r.activeValidator(ctx)
	var out []string
	for _, s := range captured {
		if v == nil || (!v.enforceSchemas && len(v.allowedSchemas) == 0) || v.allowedSchemas[strings.ToLower(s)] {
			if !slices.Contains(out, s) {
				out = append(out, s)
			}
		}
	}
	return out
}

func (r *Runner) replayStatements(ctx context.Context) []replayStmt {
	rc := replayContextFrom(ctx)
	if rc == nil {
		return nil
	}
	set := func(name, value string) replayStmt {
		return replayStmt{sql: "SELECT set_config($1, $2, true)", args: []any{name, value}}
	}
	var out []replayStmt
	if rc.Role != "" {
		out = append(out, replayStmt{sql: "SET LOCAL ROLE " + pgx.Identifier{rc.Role}.Sanitize()})
	}
	if path := r.ReplaySearchPath(ctx, rc.SearchPath); len(path) > 0 {
		quoted := make([]string, len(path))
		for i, s := range path {
			quoted[i] = pgx.Identifier{s}.Sanitize()
		}
		out = append(out, set("search_path", strings.Join(quoted, ", ")))
	}
	names := make([]string, 0, len(rc.Settings))
	for name := range rc.Settings {
		if ReplayableSetting(name) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	for _, name := range names {
		out = append(out, set(name, rc.Settings[name]))
	}
	if rc.Method != "" {
		out = append(out, set("request.method", rc.Method))
	}
	if rc.Path != "" {
		out = append(out, set("request.path", rc.Path))
	}
	return out
}

// applyReplay runs the replay context inside tx.
func (r *Runner) applyReplay(ctx context.Context, tx pgx.Tx) error {
	for _, s := range r.replayStatements(ctx) {
		if _, err := tx.Exec(ctx, s.sql, s.args...); err != nil {
			return err
		}
	}
	return nil
}

// replayBatchSQL renders the replay context for a simple-protocol batch, with
// every value as an escaped literal.
func (r *Runner) replayBatchSQL(ctx context.Context) string {
	var b strings.Builder
	for _, s := range r.replayStatements(ctx) {
		if len(s.args) == 0 {
			b.WriteString(s.sql + ";\n")
			continue
		}
		fmt.Fprintf(&b, "SELECT set_config(%s, %s, true);\n", quoteSQLLiteral(s.args[0].(string)), quoteSQLLiteral(s.args[1].(string)))
	}
	return b.String()
}

// CheckReplayRole reports whether this connection can run as role, without
// running anything as it.
func (r *Runner) CheckReplayRole(ctx context.Context, role string) error {
	pool := r.activePool(ctx)
	if pool == nil {
		return fmt.Errorf("%w: read-only pool unavailable", apperrors.ErrQueryExecutionFailed)
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return executionFailed(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+pgx.Identifier{role}.Sanitize()); err != nil {
		return executionFailed(err)
	}
	return nil
}

// executionFailed keeps the ErrQueryExecutionFailed sentinel and, for errors
// the statement itself caused, adds PostgreSQL's SQLSTATE and message. Those
// messages name only objects and values from the caller's own statement.
// Server-side classes (internal, resource, system, connection) stay opaque.
func executionFailed(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || !statementErrorClass(pgErr.Code) {
		return apperrors.ErrQueryExecutionFailed
	}
	msg := pgErr.Message
	if len(msg) > 300 {
		cut := 300
		for !utf8.RuneStart(msg[cut]) {
			cut--
		}
		msg = msg[:cut] + "..."
	}
	return fmt.Errorf("%w: SQLSTATE %s: %s", apperrors.ErrQueryExecutionFailed, pgErr.Code, msg)
}

func statementErrorClass(code string) bool {
	if len(code) != 5 {
		return false
	}
	switch code[:2] {
	case "42", "22", "0A", "54":
		return true
	}
	return code == "57014" || code == "25006"
}

// ParseSearchPath splits a search_path value such as `"api", "public"` into
// schema names. "$user" is skipped: it depends on the role, not the request.
func ParseSearchPath(s string) []string {
	var out []string
	for len(s) > 0 {
		s = strings.TrimLeft(s, " \t")
		var name string
		if strings.HasPrefix(s, `"`) {
			var b strings.Builder
			i := 1
			for i < len(s) {
				if s[i] == '"' {
					if i+1 < len(s) && s[i+1] == '"' {
						b.WriteByte('"')
						i += 2
						continue
					}
					i++
					break
				}
				b.WriteByte(s[i])
				i++
			}
			name, s = b.String(), s[i:]
		} else {
			end := strings.IndexByte(s, ',')
			if end < 0 {
				end = len(s)
			}
			name, s = strings.ToLower(strings.TrimSpace(s[:end])), s[end:]
		}
		s = strings.TrimLeft(s, " \t")
		s = strings.TrimPrefix(s, ",")
		if name != "" && name != "$user" {
			out = append(out, name)
		}
	}
	return out
}
