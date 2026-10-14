package queryrunner

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	pg_query "github.com/pganalyze/pg_query_go/v6"
)

// PostgRESTCapture is one PostgREST read request rebuilt from a PostgreSQL
// log. Nothing here is guessed from the URL: the SQL and bind values are the
// statement and parameters PostgreSQL actually executed.
//
// Request headers, cookies and JWT claims in the session-context statement are
// read past and never returned: the capture is caller-supplied log text with
// no verification it came from a real PostgreSQL log, so nothing in it that
// would let a replay impersonate a specific end user's identity is trusted.
// Only Role is returned, and only a role an operator independently granted to
// the analytical role (a real Postgres privilege, not a value from this text)
// can ever be assumed in a replay — see ReplayContext.
type PostgRESTCapture struct {
	Method     string
	Endpoint   string
	Role       string
	SearchPath string
	// ContextLogged is false when the log has no session-context statement for
	// the request (slow-query-only logging); method, path, role and search_path
	// are then unknown.
	ContextLogged bool
	QueryID       string
	SQL           string
	Binds         []string
	// StatementMs is the logged execute-phase duration; nil when the log has
	// none (log_statement without durations, or a failed statement).
	StatementMs *float64
	// Failure is PostgreSQL's error when the request statement failed, for
	// example "canceling statement due to statement timeout".
	Failure string
	// Prepared reports whether the statement was a named prepared statement,
	// which PostgreSQL may switch to a generic plan after repeated executions;
	// nil when the excerpt does not say. A failed statement recovered only
	// from the error CONTEXT line (see finish) carries its bind values but not
	// its statement name: the CONTEXT line names the PORTAL, not the prepared
	// statement, and PostgREST's portal is unnamed for essentially every
	// execution regardless of whether the underlying statement is named, so it
	// is not evidence either way.
	Prepared *bool
	// Settings are well-known, non-secret settings the request set beyond the
	// required context (Prefer: timezone, per-role settings). OtherSettings
	// counts the rest, whose names and values are not kept because per-role
	// custom settings can hold secrets.
	Settings      map[string]string
	OtherSettings int
}

// PostgRESTMainCTE is the CTE name PostgREST wraps every read statement in.
const PostgRESTMainCTE = "pgrst_source"

const postgrestMainPrefix = "WITH " + PostgRESTMainCTE + " AS"

var (
	// log_min_duration_statement logs "duration: X ms  execute N: SQL"; with
	// log_statement the statement is logged first and a bare "duration: X ms"
	// follows it.
	logExecuteRe      = regexp.MustCompile(`(?s)^(?:duration: ([0-9.]+) ms\s+)?(?:execute ([^:]+)|statement): (.*)$`)
	logBindRe         = regexp.MustCompile(`(?s)^duration: [0-9.]+ ms\s+bind ([^:]+): (.*)$`)
	logBareDurationRe = regexp.MustCompile(`^duration: ([0-9.]+) ms$`)
	logPortalParamsRe = regexp.MustCompile(`(?s)^(?:unnamed portal|portal "[^"]*") with parameters: (.*)$`)
	// A stderr record line is <log_line_prefix><SEVERITY>:  <message>. Only the
	// backend PID ([%p] or [%p-%l]) is needed from the prefix.
	logLineRe     = regexp.MustCompile(`^(|.*?[\s:])(LOG|DETAIL|ERROR|FATAL|PANIC|WARNING|NOTICE|INFO|HINT|QUERY|CONTEXT|LOCATION|STATEMENT|DEBUG[1-5]?):\s+(.*)$`)
	logPIDRe      = regexp.MustCompile(`\[(\d+)(?:-\d+)?\]`)
	logQueryIDRe  = regexp.MustCompile(`\[\d+(?:-\d+)?\] (-?\d+)(?:\s|$)`)
	logSQLStateRe = regexp.MustCompile(`^[0-9A-Z]{5}: `)
	csvLogLineRe  = regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}(?:\.\d+)? [A-Za-z0-9+:-]+,`)
	syslogLineRe  = regexp.MustCompile(`^[A-Z][a-z]{2} [ \d]\d \d{2}:\d{2}:\d{2} \S+ [^\s\[]+\[\d+\]: \[\d+-\d+\]`)
	endpointRe    = regexp.MustCompile(`^/[^?#\s]*$`)
	gucNameRe     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]{0,62}$`)
	// Settings whose values are safe to keep and matter to a replay.
	replaySettingRe = regexp.MustCompile(`^(?i:timezone|datestyle|intervalstyle|work_mem|hash_mem_multiplier|plan_cache_mode|statement_timeout|jit|effective_cache_size|random_page_cost|seq_page_cost|join_collapse_limit|from_collapse_limit|max_parallel_workers_per_gather|enable_[a-z_]+|cpu_[a-z_]+_cost|parallel_[a-z_]+_cost)$`)
)

// ErrNoPostgRESTRead means the excerpt holds no PostgREST read statement.
var ErrNoPostgRESTRead = errors.New("no PostgREST read request found in the log excerpt")

var errNoSessionContext = errors.New("no PostgREST session-context statement precedes the request in the same transaction")

// paramSet is one logged parameter list. source is "detail" (governed by
// log_parameter_max_length) or "context" (log_parameter_max_length_on_error).
type paramSet struct {
	values  []string
	present bool
	err     error
	source  string
}

type logStatement struct {
	pid, queryID, sql string
	durMs             *float64
	ps                paramSet
	failure           string
	// prepared is nil when unknown — see PostgRESTCapture.Prepared.
	prepared *bool
}

type logRecord struct {
	pid, queryID, severity, msg string
}

// ParsePostgRESTLog extracts the single PostgREST read request in a PostgreSQL
// log excerpt (stderr, csvlog or jsonlog). The excerpt must carry the request
// statement's parameters (log_min_duration_statement=0 or log_statement=all,
// log_parameter_max_length=-1). It fails closed on zero or several requests,
// non-read methods, NULL or unprovably complete values, and bind gaps.
func ParsePostgRESTLog(log string) (*PostgRESTCapture, error) {
	if len(log) > 200000 || !utf8.ValidString(log) {
		return nil, fmt.Errorf("log excerpt must be valid UTF-8 and at most 200000 bytes")
	}
	recs, err := readLogRecords(log)
	if err != nil {
		return nil, err
	}
	stmts, allParams, err := parseLogStatements(recs)
	if err != nil {
		return nil, err
	}

	var mainIdx []int
	for i, s := range stmts {
		if strings.HasPrefix(s.sql, postgrestMainPrefix) {
			mainIdx = append(mainIdx, i)
		}
	}
	switch len(mainIdx) {
	case 0:
		return nil, ErrNoPostgRESTRead
	case 1:
	default:
		var pids []string
		for _, i := range mainIdx {
			if !slices.Contains(pids, stmts[i].pid) {
				pids = append(pids, stmts[i].pid)
			}
		}
		return nil, fmt.Errorf("log excerpt holds %d PostgREST read requests (backend PIDs %s); include exactly one: keep one backend's lines (its [PID]) from one request's BEGIN to COMMIT", len(mainIdx), strings.Join(pids, ", "))
	}
	main := stmts[mainIdx[0]]
	if _, _, ok := parseSingleSelect(main.sql); !ok {
		return nil, fmt.Errorf("captured statement is not a single SELECT")
	}
	out := &PostgRESTCapture{QueryID: main.queryID, SQL: main.sql, StatementMs: main.durMs, Failure: main.failure, Prepared: main.prepared}

	ctx, err := sessionContextBefore(stmts, mainIdx[0])
	switch {
	case errors.Is(err, errNoSessionContext):
		// Slow-query-only logging records the request statement but not
		// PostgREST's fast set_config statement: provenance stays unknown.
	case err != nil:
		return nil, err
	default:
		if ctx.method != "GET" && ctx.method != "HEAD" {
			return nil, fmt.Errorf("only GET and HEAD read requests are supported")
		}
		if !endpointRe.MatchString(ctx.path) || len(ctx.path) > 2048 {
			return nil, fmt.Errorf("captured request path is not a plain endpoint path")
		}
		contextValues := []string{ctx.method, ctx.path, ctx.role, ctx.searchPath, ctx.claims}
		for _, v := range ctx.settings {
			contextValues = append(contextValues, v)
		}
		if i := unprovenTruncation(contextValues, "detail", allParams); i >= 0 {
			return nil, fmt.Errorf("a session-context value ends in '...'; %s", truncationAdvice)
		}
		out.ContextLogged = true
		out.Method, out.Endpoint, out.Role, out.SearchPath = ctx.method, ctx.path, ctx.role, ctx.searchPath
		out.Settings, out.OtherSettings = ctx.settings, ctx.otherSettings
	}

	// PostgreSQL logs no parameters at all for a statement with zero ParamRef
	// nodes, so their absence is only missing evidence when the AST uses $N.
	n := maxParamNumber(main.sql)
	switch {
	case n == 0 && !main.ps.present:
	case !main.ps.present && main.failure != "":
		return nil, fmt.Errorf("the failed request statement's parameters are not in the log; set log_parameter_max_length_on_error=-1, or log_min_duration_statement=0 (which logs them at bind)")
	case !main.ps.present:
		return nil, fmt.Errorf("log has no parameters for the request statement; set log_parameter_max_length=-1")
	case main.ps.err != nil:
		return nil, main.ps.err
	default:
		out.Binds = main.ps.values
	}
	if n != len(out.Binds) {
		return nil, fmt.Errorf("captured statement uses $%d but the log carries %d bind value(s)", n, len(out.Binds))
	}
	if i := unprovenTruncation(out.Binds, main.ps.source, allParams); i >= 0 {
		return nil, fmt.Errorf("bind $%d's logged value ends in '...'; %s", i+1, truncationAdvice)
	}
	return out, nil
}

const truncationAdvice = "PostgreSQL marks a value cut by log_parameter_max_length the same way, and no longer, untruncated parameter in the excerpt proves this one was not cut. Recapture with log_parameter_max_length=-1 and include the session-context statement, whose longer values prove nothing was cut"

// unprovenTruncation returns the index of a value ending in "..." that could
// be PostgreSQL's truncation marker, or -1. PostgreSQL clips a value to at
// most maxlen bytes and appends "...", so a truncated value is at least maxlen
// bytes long including the marker, while every untruncated value from the same
// setting is at most maxlen bytes. An untruncated value longer than v therefore
// proves v was not cut.
func unprovenTruncation(values []string, source string, all []paramSet) int {
	longest := 0
	for _, ps := range all {
		if ps.source != source {
			continue
		}
		for _, v := range ps.values {
			if !strings.HasSuffix(v, "...") && len(v) > longest {
				longest = len(v)
			}
		}
	}
	for i, v := range values {
		if strings.HasSuffix(v, "...") && len(v) >= longest {
			return i
		}
	}
	return -1
}

// readLogRecords detects the log format and returns its records in order.
// Structured formats carry DETAIL, CONTEXT and STATEMENT on the same record;
// they are emitted as separate records so every format is read the same way.
func readLogRecords(log string) ([]logRecord, error) {
	first := ""
	for _, line := range strings.Split(log, "\n") {
		if first = strings.TrimSpace(line); first != "" {
			break
		}
	}
	switch {
	case strings.HasPrefix(first, "{"):
		return readJSONLog(log)
	case csvLogLineRe.MatchString(first):
		return readCSVLog(log)
	case syslogLineRe.MatchString(first):
		return nil, fmt.Errorf("syslog-forwarded logs are not supported: syslog splits long statements into chunks and escapes tabs, so the exact SQL and values cannot be rebuilt; capture stderr, csvlog or jsonlog output instead")
	}
	return splitLogRecords(log)
}

func readJSONLog(log string) ([]logRecord, error) {
	var recs []logRecord
	for _, line := range strings.Split(log, "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		var e struct {
			PID       json.Number `json:"pid"`
			QueryID   json.Number `json:"query_id"`
			Severity  string      `json:"error_severity"`
			Message   string      `json:"message"`
			Detail    string      `json:"detail"`
			Context   string      `json:"context"`
			Statement string      `json:"statement"`
		}
		dec := json.NewDecoder(strings.NewReader(line))
		dec.UseNumber()
		if err := dec.Decode(&e); err != nil || e.PID == "" || e.Severity == "" {
			return nil, fmt.Errorf("unsupported jsonlog line; include complete one-object-per-line jsonlog records")
		}
		recs = append(recs, structuredRecords(e.PID.String(), e.QueryID.String(), e.Severity, e.Message, e.Detail, e.Context, e.Statement)...)
	}
	return recs, nil
}

// readCSVLog reads csvlog columns process_id(3), error_severity(11),
// message(13), detail(14), context(18), query(19) and query_id(25, PG14+).
func readCSVLog(log string) ([]logRecord, error) {
	r := csv.NewReader(strings.NewReader(log))
	r.FieldsPerRecord = -1
	var recs []logRecord
	for {
		f, err := r.Read()
		if errors.Is(err, io.EOF) {
			return recs, nil
		}
		if err != nil || len(f) < 23 {
			return nil, fmt.Errorf("unsupported csvlog record; include complete csvlog records")
		}
		var qid string
		if len(f) >= 26 {
			qid = f[25]
		}
		recs = append(recs, structuredRecords(f[3], qid, f[11], f[13], f[14], f[18], f[19])...)
	}
}

func structuredRecords(pid, qid, severity, msg, detail, context, statement string) []logRecord {
	if qid == "0" {
		qid = ""
	}
	out := []logRecord{{pid: pid, queryID: qid, severity: severity, msg: msg}}
	for _, part := range [][2]string{{"DETAIL", detail}, {"CONTEXT", context}, {"STATEMENT", statement}} {
		if part[1] != "" {
			out = append(out, logRecord{pid: pid, queryID: qid, severity: part[0], msg: part[1]})
		}
	}
	return out
}

// splitLogRecords groups stderr-format lines into records. PostgreSQL writes
// every newline inside a message as newline + tab, so stripping that one tab
// restores the message exactly (PostgREST's CSV statement and other clients'
// formatted SQL are multi-line). Records are tied to a request by backend PID,
// not application_name, which managed services may not log.
func splitLogRecords(log string) ([]logRecord, error) {
	// A copy that turned every line ending into CRLF is normalized; a lone CR is data.
	if strings.Count(log, "\r\n") == strings.Count(log, "\n") {
		log = strings.ReplaceAll(log, "\r\n", "\n")
	}
	var recs []logRecord
	for _, line := range strings.Split(log, "\n") {
		if rest, ok := strings.CutPrefix(line, "\t"); ok {
			// A continuation before the first record belongs to a record outside the excerpt.
			if len(recs) > 0 {
				recs[len(recs)-1].msg += "\n" + rest
			}
			continue
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		m := logLineRe.FindStringSubmatch(line)
		if m == nil {
			return nil, fmt.Errorf("unsupported log line; include complete PostgreSQL stderr, csvlog or jsonlog records (stderr continuation lines start with a tab)")
		}
		pid := logPIDRe.FindStringSubmatch(m[1])
		if pid == nil {
			return nil, fmt.Errorf("log line has no backend process id; set log_line_prefix to include [%%p]")
		}
		var qid string
		if q := logQueryIDRe.FindStringSubmatch(m[1]); q != nil && q[1] != "0" {
			qid = q[1]
		}
		recs = append(recs, logRecord{pid: pid[1], queryID: qid, severity: m[2], msg: logSQLStateRe.ReplaceAllString(m[3], "")})
	}
	return recs, nil
}

type loggedBind struct {
	sql      string
	ps       paramSet
	prepared bool
}

type openFailure struct {
	msg, queryID string
	ps           paramSet
	// ownExecIdx is the stmts index of this backend's own just-logged execute
	// (log_statement logs it at statement start, before the outcome is known),
	// captured at the instant this failure opened, or -1 when none was pending
	// right then. It must be captured then, not re-derived later from "the last
	// stmts entry for this pid" — on a backend a pooler hands to many
	// sequential requests over its lifetime, PostgREST reuses the identical
	// SQL text for every call to the same endpoint shape, so a later, unrelated
	// lookup by text alone would just as readily match an earlier, already
	// -finished request's entry as this failure's own.
	ownExecIdx int
}

// pidState tracks one backend: records of different backends interleave.
type pidState struct {
	detailStmt int         // statement whose DETAIL parameters come next, or -1
	detailBind *loggedBind // bind whose DETAIL parameters come next
	durStmt    int         // statement waiting for a bare duration record, or -1
	// openExecIdx is the stmts index of an execute just logged for this
	// backend, valid only until the very next record for this backend (see the
	// main loop): either it is claimed by an ERROR opening right then (log_
	// statement logged the execute before it failed), or it is cleared,
	// because anything else means that execute's fate is already settled and
	// it must never be mistaken for a later, different request's.
	openExecIdx int
	// lastBind rescues a failed statement's parameters when its own execute
	// never got logged (see finish). It must never survive past the point its
	// bind is resolved one way or another, or a backend that a connection
	// pooler hands to many sequential, unrelated requests over its lifetime
	// could attribute an earlier, already-finished request's values to a later
	// one that merely reuses the same SQL text (PostgREST does, for every call
	// to the same endpoint shape). It is cleared the moment any statement of
	// this backend's completes execution — including BEGIN/COMMIT/ROLLBACK,
	// which are logged as ordinary statements — and again when a failure it
	// may have rescued closes, so it is live only across the exact gap between
	// a bind and that same bind's own failure, never beyond.
	lastBind *loggedBind
	failure  *openFailure // ERROR group not yet closed by STATEMENT
}

// parseLogStatements turns records into executed statements with their
// parameters, durations and failures, in log order. A request statement that
// failed has no execute record; it is rebuilt from the ERROR group's STATEMENT
// and the parameters of its bind (or the error CONTEXT). Only ERROR severity
// counts: parallel workers log FATAL with the same statement text.
func parseLogStatements(recs []logRecord) ([]logStatement, []paramSet, error) {
	var stmts []logStatement
	var all []paramSet
	states := map[string]*pidState{}
	state := func(pid string) *pidState {
		if states[pid] == nil {
			states[pid] = &pidState{detailStmt: -1, durStmt: -1, openExecIdx: -1}
		}
		return states[pid]
	}
	params := func(list, source string) paramSet {
		values, err := splitLogParams(list)
		ps := paramSet{values: values, present: true, err: err, source: source}
		all = append(all, ps)
		return ps
	}
	finish := func(pid, statement string) {
		s := state(pid)
		f := s.failure
		if f == nil {
			return
		}
		s.failure = nil
		if strings.HasPrefix(statement, postgrestMainPrefix) {
			if i := f.ownExecIdx; i >= 0 && i < len(stmts) && stmts[i].sql == statement && stmts[i].failure == "" {
				// log_statement logged the execute before it failed.
				stmts[i].failure = f.msg
			} else {
				failed := logStatement{pid: pid, queryID: f.queryID, sql: statement, failure: f.msg}
				switch {
				case s.lastBind != nil && s.lastBind.sql == statement && s.lastBind.ps.present:
					prepared := s.lastBind.prepared
					failed.ps, failed.prepared = s.lastBind.ps, &prepared
				case f.ps.present:
					// Recovered only from the error CONTEXT line: it carries bind
					// values but, unlike a bind/execute log line, never the
					// statement's name, so failed.prepared stays nil (unknown)
					// rather than defaulting to a confident "no".
					failed.ps = f.ps
				}
				stmts = append(stmts, failed)
			}
		}
		// This failure's transaction is closing (the ROLLBACK below): whatever
		// bind was pending belonged to it, used or not, and must not be offered
		// to a later, unrelated failure on the same backend.
		s.lastBind = nil
		stmts = append(stmts, logStatement{pid: pid, sql: "ROLLBACK"})
	}

	for _, r := range recs {
		s := state(r.pid)
		switch r.severity {
		case "DETAIL":
			// PostgreSQL 16 emits "parameters", whereas 18 emits "Parameters".
			const prefix = "parameters: "
			if len(r.msg) >= len(prefix) && strings.EqualFold(r.msg[:len(prefix)], prefix) {
				switch {
				case s.detailStmt >= 0:
					stmts[s.detailStmt].ps = params(r.msg[len(prefix):], "detail")
				case s.detailBind != nil:
					s.detailBind.ps = params(r.msg[len(prefix):], "detail")
				}
			}
			s.detailStmt, s.detailBind = -1, nil
			continue
		case "CONTEXT":
			if m := logPortalParamsRe.FindStringSubmatch(r.msg); m != nil && s.failure != nil {
				s.failure.ps = params(m[1], "context")
			}
			continue
		case "STATEMENT":
			finish(r.pid, strings.TrimSpace(r.msg))
			continue
		case "HINT", "QUERY", "LOCATION":
			continue
		}
		// A new primary record closes any open error group and pending DETAIL,
		// and settles any execute left pending by the previous record: this
		// record either claims it (ERROR, right below) or supersedes it.
		finish(r.pid, "")
		ownExecIdx := s.openExecIdx
		s.detailStmt, s.detailBind, s.openExecIdx = -1, nil, -1
		switch r.severity {
		case "ERROR":
			s.failure = &openFailure{msg: r.msg, queryID: r.queryID, ownExecIdx: ownExecIdx}
			continue
		case "FATAL", "PANIC":
			// A crashed/terminated backend is as much a boundary as a graceful
			// failure closing in finish(): whatever bind was pending belonged
			// to a connection that no longer exists, and must not be offered
			// to whatever logically unrelated request a connection pooler
			// later maps to this same PID.
			s.lastBind = nil
			stmts = append(stmts, logStatement{pid: r.pid, sql: "ROLLBACK"})
			continue
		case "LOG":
		default:
			continue
		}
		if d := logBareDurationRe.FindStringSubmatch(r.msg); d != nil {
			if s.durStmt >= 0 {
				dur, err := parseLogDuration(d[1])
				if err != nil {
					return nil, nil, err
				}
				stmts[s.durStmt].durMs = dur
				s.durStmt = -1
			}
			continue
		}
		if b := logBindRe.FindStringSubmatch(r.msg); b != nil {
			s.lastBind = &loggedBind{sql: strings.TrimSpace(b[2]), prepared: namedStatement(b[1])}
			s.detailBind = s.lastBind
			continue
		}
		if e := logExecuteRe.FindStringSubmatch(r.msg); e != nil {
			prepared := namedStatement(e[2])
			st := logStatement{pid: r.pid, queryID: r.queryID, sql: strings.TrimSpace(e[3]), prepared: &prepared}
			s.durStmt = -1
			if e[1] != "" {
				dur, err := parseLogDuration(e[1])
				if err != nil {
					return nil, nil, err
				}
				st.durMs = dur
			}
			stmts = append(stmts, st)
			s.detailStmt = len(stmts) - 1
			if st.durMs == nil {
				// No duration yet: log_statement logged this at statement start,
				// before its outcome is known. It stays eligible to be claimed
				// by an ERROR that opens on the very next record (see finish);
				// anything else settles it (see the "new primary record" reset).
				s.durStmt = len(stmts) - 1
				s.openExecIdx = len(stmts) - 1
			}
			// This statement completed executing, resolving any bind that was
			// still pending for this backend (see pidState.lastBind); it is no
			// longer eligible to rescue a later, different failure.
			s.lastBind = nil
		}
	}
	for pid := range states {
		finish(pid, "")
	}
	return stmts, all, nil
}

// namedStatement reports whether a logged "execute NAME" or "bind NAME" refers
// to a named prepared statement ("2", "S_1/C_1"), not "<unnamed>".
func namedStatement(name string) bool {
	return name != "" && !strings.HasPrefix(name, "<unnamed>")
}

type sessionContext struct {
	searchPath, role, method, path, claims string
	settings                               map[string]string
	otherSettings                          int
}

// sessionContextBefore never crosses a transaction boundary on a pooled backend.
// It returns errNoSessionContext when the transaction's set_config statement is
// not in the log.
func sessionContextBefore(stmts []logStatement, idx int) (sessionContext, error) {
	for i := idx - 1; i >= 0; i-- {
		s := stmts[i]
		if s.pid != stmts[idx].pid {
			continue
		}
		upper := strings.ToUpper(s.sql)
		for _, boundary := range []string{"BEGIN", "START TRANSACTION", "COMMIT", "END", "ROLLBACK", "ABORT"} {
			if strings.HasPrefix(upper, boundary) {
				return sessionContext{}, errNoSessionContext
			}
		}
		if !strings.Contains(s.sql, "set_config") {
			continue
		}
		if !s.ps.present {
			return sessionContext{}, fmt.Errorf("log has no parameters for the session-context statement; set log_parameter_max_length=-1")
		}
		if s.ps.err != nil {
			return sessionContext{}, s.ps.err
		}
		params := s.ps.values
		// Resolve values by the AST's parameter references, not their position.
		// A changed PostgREST layout must not put a credential into the role/path.
		// PostgREST appends set_config calls for optional per-request settings
		// (Prefer: timezone) and per-role settings, whose names are themselves
		// parameters (set_config($2, $3, true)), so the 7 required keys checked
		// below are a floor, not an exact count.
		_, sel, ok := parseSingleSelect(s.sql)
		if !ok || len(sel.FromClause) != 0 || len(sel.TargetList) < 7 || len(sel.TargetList) > 64 {
			return sessionContext{}, fmt.Errorf("unsupported PostgREST session-context statement")
		}
		param := func(n int32) (string, bool) {
			if n < 1 || int(n) > len(params) {
				return "", false
			}
			return params[n-1], true
		}
		settings := map[string]string{}
		for _, target := range sel.TargetList {
			fn := target.GetResTarget().GetVal().GetFuncCall()
			if fn == nil || len(fn.Funcname) != 1 || fn.Funcname[0].GetString_().GetSval() != "set_config" || len(fn.Args) != 3 {
				return sessionContext{}, fmt.Errorf("unsupported PostgREST session-context statement")
			}
			key := fn.Args[0].GetAConst().GetSval().GetSval()
			if ref := fn.Args[0].GetParamRef(); ref != nil {
				key, _ = param(ref.GetNumber())
			}
			key = strings.ToLower(key)
			value, ok := param(fn.Args[1].GetParamRef().GetNumber())
			if _, dup := settings[key]; dup || !ok || !gucNameRe.MatchString(key) || !fn.Args[2].GetAConst().GetBoolval().GetBoolval() {
				return sessionContext{}, fmt.Errorf("invalid PostgREST session-context parameters")
			}
			settings[key] = value
		}
		required := []string{"search_path", "role", "request.jwt.claims", "request.method", "request.path", "request.headers", "request.cookies"}
		for _, key := range required {
			if _, ok := settings[key]; !ok {
				return sessionContext{}, fmt.Errorf("incomplete PostgREST session context")
			}
		}
		if settings["role"] == "" || settings["search_path"] == "" {
			return sessionContext{}, fmt.Errorf("missing PostgREST role or search_path")
		}
		out := sessionContext{searchPath: settings["search_path"], role: settings["role"], method: settings["request.method"], path: settings["request.path"], claims: settings["request.jwt.claims"]}
		for key, value := range settings {
			switch {
			case slices.Contains(required, key):
			case replaySettingRe.MatchString(key):
				if out.settings == nil {
					out.settings = map[string]string{}
				}
				out.settings[key] = value
			default:
				out.otherSettings++
			}
		}
		return out, nil
	}
	return sessionContext{}, errNoSessionContext
}

// splitLogParams rejects gaps, duplicates, NULLs and unconsumed input. The
// quoted string 'NULL' is distinct from an SQL NULL parameter, which PostgREST
// does not send. A value ending in "..." is checked by the caller against the
// rest of the excerpt (see unprovenTruncation).
func splitLogParams(s string) ([]string, error) {
	var out []string
	for len(s) > 0 {
		prefix := fmt.Sprintf("$%d = ", len(out)+1)
		if !strings.HasPrefix(s, prefix) {
			return nil, fmt.Errorf("bind numbers must be contiguous and ordered from $1")
		}
		s = strings.TrimPrefix(s, prefix)
		if strings.HasPrefix(s, "NULL") {
			return nil, fmt.Errorf("NULL binds are not supported")
		}
		if len(s) == 0 || s[0] != '\'' {
			return nil, fmt.Errorf("invalid or truncated bind value")
		}
		s = s[1:]
		var b strings.Builder
		closed := false
		for len(s) > 0 {
			if s[0] == '\'' {
				if len(s) > 1 && s[1] == '\'' {
					b.WriteByte('\'')
					s = s[2:]
					continue
				}
				s = s[1:]
				closed = true
				break
			}
			b.WriteByte(s[0])
			s = s[1:]
		}
		if !closed {
			return nil, fmt.Errorf("invalid or truncated bind value")
		}
		out = append(out, b.String())
		if s == "" {
			return out, nil
		}
		if !strings.HasPrefix(s, ", ") || len(s) == 2 {
			return nil, fmt.Errorf("invalid or truncated bind parameter list")
		}
		s = s[2:]
	}
	return out, nil
}

func parseLogDuration(s string) (*float64, error) {
	d, err := strconv.ParseFloat(s, 64)
	if err != nil || !validDuration(d) {
		return nil, fmt.Errorf("invalid statement duration")
	}
	return &d, nil
}

func validDuration(d float64) bool {
	return d >= 0 && !math.IsNaN(d) && !math.IsInf(d, 0)
}

// IsPostgRESTWrapper reports whether sql is PostgREST's generated response
// wrapper (the pgrst_source CTE with json_agg). Its outer shape is not user SQL,
// so rewrite suggestions do not apply to it; the change belongs in the relation
// it reads or in the request.
func IsPostgRESTWrapper(sql string) bool {
	_, sel, ok := parseSingleSelect(sql)
	if !ok || sel.WithClause == nil {
		return false
	}
	for _, c := range sel.WithClause.Ctes {
		if cte := c.GetCommonTableExpr(); cte != nil && cte.Ctename == PostgRESTMainCTE {
			return true
		}
	}
	return false
}

// CollectRelations returns the schema-qualified base relations a statement reads,
// excluding its own CTE names. Used to say where a change would be applied.
func CollectRelations(sql string) ([]string, error) {
	treeJSON, err := pg_query.ParseToJSON(strings.TrimSpace(sql))
	if err != nil {
		return nil, err
	}
	var tree map[string]interface{}
	if err := json.Unmarshal([]byte(treeJSON), &tree); err != nil {
		return nil, err
	}
	ctes := collectCTENames(tree)
	seen := map[string]struct{}{}
	var out []string
	collectRelationNamesInto(tree, ctes, seen, &out)
	sort.Strings(out)
	return out, nil
}

func collectRelationNamesInto(node interface{}, ctes map[string]struct{}, seen map[string]struct{}, out *[]string) {
	switch n := node.(type) {
	case map[string]interface{}:
		for key, value := range n {
			if key == "RangeVar" {
				rv, ok := value.(map[string]interface{})
				if !ok {
					continue
				}
				rel, _ := rv["relname"].(string)
				schema, _ := rv["schemaname"].(string)
				if _, isCTE := ctes[rel]; (isCTE && schema == "") || rel == "" {
					continue
				}
				name := rel
				if schema, _ := rv["schemaname"].(string); schema != "" {
					name = schema + "." + rel
				}
				if _, dup := seen[name]; !dup {
					seen[name] = struct{}{}
					*out = append(*out, name)
				}
				continue
			}
			collectRelationNamesInto(value, ctes, seen, out)
		}
	case []interface{}:
		for _, item := range n {
			collectRelationNamesInto(item, ctes, seen, out)
		}
	}
}

// CollectFunctions returns the schema-qualified functions a statement calls
// outside pg_catalog, such as a PostgREST RPC.
func CollectFunctions(sql string) ([]string, error) {
	treeJSON, err := pg_query.ParseToJSON(strings.TrimSpace(sql))
	if err != nil {
		return nil, err
	}
	var tree interface{}
	if err := json.Unmarshal([]byte(treeJSON), &tree); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	var walk func(interface{})
	walk = func(node interface{}) {
		switch n := node.(type) {
		case map[string]interface{}:
			if fc, ok := n["FuncCall"].(map[string]interface{}); ok {
				if parts, ok := fc["funcname"].([]interface{}); ok && len(parts) == 2 {
					var names []string
					for _, p := range parts {
						str, _ := p.(map[string]interface{})["String"].(map[string]interface{})
						sval, _ := str["sval"].(string)
						names = append(names, sval)
					}
					if name := strings.Join(names, "."); names[0] != "pg_catalog" && names[0] != "" && names[1] != "" && !seen[name] {
						seen[name] = true
						out = append(out, name)
					}
				}
			}
			for _, v := range n {
				walk(v)
			}
		case []interface{}:
			for _, v := range n {
				walk(v)
			}
		}
	}
	walk(tree)
	sort.Strings(out)
	return out, nil
}

// postgrestJSONPayloadRe matches PostgREST's fixed internal shape for its
// older RPC JSON-payload wrapper: a one-column subquery aliased pgrst_payload
// whose sole column, json_data, is a bare parameter reference.
var postgrestJSONPayloadRe = regexp.MustCompile(`\(SELECT \$(\d+) AS json_data\) pgrst_payload`)

// postgrestJSONPayloadCast returns the 1-based bind number and JSON type
// (json or jsonb) that must be cast explicitly when substituting into sql's
// RPC JSON-payload wrapper, or (0, "") when sql does not use that shape.
func postgrestJSONPayloadCast(sql string) (int, string) {
	m := postgrestJSONPayloadRe.FindStringSubmatch(sql)
	if m == nil {
		return 0, ""
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, ""
	}
	switch {
	case strings.Contains(sql, "jsonb_to_record(pgrst_payload.json_data)"):
		return n, "jsonb"
	case strings.Contains(sql, "json_to_record(pgrst_payload.json_data)"):
		return n, "json"
	}
	return 0, ""
}

// ServerTimingDuration returns the dur (ms) of one metric from a Server-Timing
// header value, e.g. ServerTimingDuration("jwt;dur=0.6, transaction;dur=54.8", "transaction").
func ServerTimingDuration(header, metric string) (float64, bool) {
	for _, part := range strings.Split(header, ",") {
		fields := strings.Split(strings.TrimSpace(part), ";")
		if len(fields) == 0 || strings.TrimSpace(fields[0]) != metric {
			continue
		}
		for _, f := range fields[1:] {
			f = strings.TrimSpace(f)
			if v, ok := strings.CutPrefix(f, "dur="); ok {
				if d, err := strconv.ParseFloat(v, 64); err == nil && validDuration(d) {
					return d, true
				}
			}
		}
	}
	return 0, false
}
