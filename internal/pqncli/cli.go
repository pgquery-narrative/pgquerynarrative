package pqncli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/pgquerynarrative/pgquerynarrative/app/queryrunner"
)

// Version is set by the linker.
var Version = "dev"

// Connector opens the database. It is a parameter so tests can supply a fake.
type Connector func(ctx context.Context, primaryDSN, replicaDSN string) (Backend, error)

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

const usageText = `pqn: find out why a PostgreSQL query is slow, get a proposal from its plan, and see it proven.

Usage: pqn <command> [flags]   (flags may come before or after the statement; quote a statement
                                  that has a word starting with -, or put it after --)

  doctor          Check that the setup is safe (pqn_api.verify_setup)
  top             The statements that cost the most (from pg_stat_statements)
  plan            Estimated plan and findings for one statement. Executes nothing
  run             Run one read-only statement over the views you may see
  investigate     Plan it, name what is wrong, propose fixes from the plan, prove them
  prove           Prove a rewrite you wrote: same rows, then faster
  investigations  Your recorded investigations
  evidence <id>   The evidence recorded for one investigation
  version

Connection (every command): pqn logs in as you. It stores no secret.
  --dsn <dsn>       primary. Default: $PQN_DSN, then the libpq environment (PGHOST, PGUSER, PGSERVICE, ~/.pgpass)
  --replica <dsn>   optional second server for heavy reads. Default: $PQN_REPLICA_DSN
Reads (plan, run, measuring) use the replica when there is one. Writes (the ledger) and the
workload statistics use the primary.

Exit codes: 0 done (investigate and prove: a proposal was proven), 2 nothing proven, 1 error.
`

type common struct {
	dsn     string
	replica string
	asJSON  bool
	timeout time.Duration
}

func addCommon(fs *flag.FlagSet, c *common, getenv func(string) string) {
	fs.StringVar(&c.dsn, "dsn", getenv("PQN_DSN"), "primary connection string")
	fs.StringVar(&c.replica, "replica", getenv("PQN_REPLICA_DSN"), "replica connection string")
	fs.BoolVar(&c.asJSON, "json", false, "print JSON")
	fs.DurationVar(&c.timeout, "timeout", 10*time.Minute, "give up after this long")
}

// parseInterspersed parses flags wherever they appear, so `pqn run "select 1" --json` works like
// `pqn run --json "select 1"`, and returns the remaining words. A bare `--` before any word ends the
// flags; after a word it is kept, because in a statement it starts a comment. Everything after it is
// verbatim. A token like -1 is a word, so `pqn run SELECT -1` works.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var flags, words []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--" && len(words) == 0:
			words = append(words, args[i+1:]...)
			i = len(args)
		case a == "--":
			words = append(words, args[i:]...)
			i = len(args)
		case !strings.HasPrefix(a, "-") || a == "-" || isNumber(a[1:]):
			words = append(words, a)
		default:
			flags = append(flags, a)
			name, _, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
			if f := fs.Lookup(name); f != nil && !hasValue && i+1 < len(args) {
				if b, ok := f.Value.(interface{ IsBoolFlag() bool }); !ok || !b.IsBoolFlag() {
					i++
					flags = append(flags, args[i])
				}
			}
		}
	}
	return words, fs.Parse(flags)
}

func isNumber(s string) bool {
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}

// Main runs the tool and returns the exit code.
func Main(args []string, stdout, stderr io.Writer, getenv func(string) string, connect Connector) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(stdout, usageText)
		return 0
	}
	stdout, stderr = safeWriter{stdout}, safeWriter{stderr}
	cmd, rest := args[0], args[1:]
	if cmd == "version" {
		fmt.Fprintln(stdout, "pqn", Version)
		return 0
	}

	fs := flag.NewFlagSet("pqn "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var c common
	addCommon(fs, &c, getenv)
	var (
		n         = fs.Int("n", 20, "how many rows")
		title     = fs.String("title", "", "title for the ledger")
		sqlFlag   = fs.String("sql", "", "the statement")
		file      = fs.String("file", "", "read the statement from a file")
		queryID   = fs.Int64("queryid", 0, "queryid from `pqn top`")
		binds     stringList
		noRecord  = fs.Bool("no-record", false, "do not write to the ledger")
		maxCand   = fs.Int("max-candidates", defaultMaxCandidates, "most proposals to consider")
		maxProofs = fs.Int("max-proofs", defaultMaxProofs, "most proposals to measure")
		before    = fs.String("before", "", "prove: the original statement")
		after     = fs.String("after", "", "prove: your rewrite")
		invID     = fs.Int64("id", 0, "prove: add to this investigation")
	)
	fs.Var(&binds, "bind", "value for $1, $2, ... (repeat the flag once per placeholder)")
	positional, err := parseInterspersed(fs, rest)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		if strings.Contains(err.Error(), "not defined") {
			fmt.Fprintln(stderr, "pqn: if that was part of the statement, put the statement in quotes or after --")
		}
		return 1
	}
	fail := func(err error) int {
		fmt.Fprintln(stderr, "pqn:", err)
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()

	statement := func() (string, error) {
		given := 0
		for _, set := range []bool{*sqlFlag != "", *file != "", len(positional) > 0} {
			if set {
				given++
			}
		}
		switch {
		case given > 1:
			return "", errors.New("give the statement once: --sql, --file, or the words after the command")
		case *sqlFlag != "":
			return *sqlFlag, nil
		case *file != "":
			b, err := os.ReadFile(*file)
			return string(b), err
		}
		return strings.Join(positional, " "), nil
	}

	switch cmd {
	case "plan", "run", "investigate": // the statement may be words
	case "evidence":
		if len(positional) > 1 {
			return fail(fmt.Errorf("evidence takes one investigation id, got %d arguments", len(positional)))
		}
	case "doctor", "top", "prove", "investigations":
		if len(positional) > 0 {
			return fail(fmt.Errorf("%s takes no arguments, got %q", cmd, strings.Join(positional, " ")))
		}
	default:
		fmt.Fprintf(stderr, "pqn: unknown command %q\n\n%s", cmd, usageText)
		return 1
	}

	be, err := connect(ctx, c.dsn, c.replica)
	if err != nil {
		return fail(err)
	}
	defer be.Close()
	val := queryrunner.NewValidator(nil, 100000)

	switch cmd {
	case "doctor":
		rows, err := be.Doctor(ctx)
		if err != nil {
			return fail(err)
		}
		if c.asJSON {
			return printJSON(stdout, rows)
		}
		return renderDoctor(stdout, rows)

	case "top":
		rows, err := be.Top(ctx, *n)
		if err != nil {
			return fail(err)
		}
		if c.asJSON {
			return printJSON(stdout, rows)
		}
		out := make([][]string, 0, len(rows))
		for _, r := range rows {
			out = append(out, []string{strconv.FormatInt(r.QueryID, 10), strconv.FormatInt(r.Calls, 10),
				ms(r.MeanMs), ms(r.TotalMs), r.Query})
		}
		Table(stdout, []string{"queryid", "calls", "mean", "total", "statement"}, out, 80)
		if len(rows) > 0 {
			fmt.Fprintln(stdout, "\nInvestigate one:  pqn investigate --queryid", rows[0].QueryID)
		}
		return 0

	case "plan":
		sql, err := statement()
		if err != nil {
			return fail(err)
		}
		if strings.TrimSpace(sql) == "" {
			return fail(errors.New("give a statement: pqn plan \"SELECT ...\""))
		}
		sql = tidySQL(sql)
		if err := val.Validate(sql); err != nil {
			return fail(fmt.Errorf("the statement is not accepted: %w", err))
		}
		exec := sql
		if placeholderRe.MatchString(sql) && len(binds) > 0 {
			if exec, err = queryrunner.SubstituteParams(sql, binds); err != nil {
				return fail(err)
			}
		}
		plan, err := be.Plan(ctx, exec)
		if err != nil {
			return fail(err)
		}
		if c.asJSON {
			fmt.Fprintln(stdout, string(plan))
			return 0
		}
		a, err := be.Analyze(ctx, plan)
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "Estimated plan, total cost %.0f\n", a.TotalCost)
		if len(a.Findings) == 0 {
			fmt.Fprintln(stdout, "No findings.")
		}
		for _, f := range a.Findings {
			fmt.Fprintf(stdout, "\n[%s] %s", f.Confidence, f.Category)
			if f.Relation != "" {
				fmt.Fprintf(stdout, " on %s", f.Relation)
			}
			fmt.Fprintln(stdout)
			printWrapped(stdout, "  ", f.Message, 94)
		}
		fmt.Fprintln(stdout, "\nNext: pqn investigate to get a proposal and a proof.")
		return 0

	case "run":
		sql, err := statement()
		if err != nil {
			return fail(err)
		}
		if strings.TrimSpace(sql) == "" {
			return fail(errors.New("give a statement: pqn run \"SELECT ...\""))
		}
		sql = tidySQL(sql)
		if err := val.Validate(sql); err != nil {
			return fail(fmt.Errorf("the statement is not accepted: %w", err))
		}
		res, err := be.Run(ctx, sql, *n)
		if err != nil {
			return fail(err)
		}
		if c.asJSON {
			return printJSON(stdout, res)
		}
		out := make([][]string, 0, len(res.Rows))
		for _, row := range res.Rows {
			cells := make([]string, len(res.Columns))
			for i, col := range res.Columns {
				cells[i] = cell(row[col])
			}
			out = append(out, cells)
		}
		Table(stdout, res.Columns, out, 60)
		fmt.Fprintf(stdout, "(%d row(s)%s)\n", len(res.Rows), map[bool]string{true: ", truncated at the limit", false: ""}[res.Truncated])
		return 0

	case "investigate":
		sql, err := statement()
		if err != nil {
			return fail(err)
		}
		opt := InvestigateOptions{SQL: sql, Binds: binds, Title: *title, MaxCandidates: *maxCand, MaxProofs: *maxProofs, NoRecord: *noRecord}
		if *queryID != 0 {
			opt.QueryID = queryID
		}
		rep, err := Investigate(ctx, be, val, opt)
		if err != nil {
			return fail(err)
		}
		if c.asJSON {
			printJSON(stdout, rep)
		} else {
			rep.Render(stdout)
		}
		return rep.ExitCode()

	case "prove":
		if strings.TrimSpace(*before) == "" || strings.TrimSpace(*after) == "" {
			return fail(errors.New("give --before and --after, each a complete statement"))
		}
		b, a := tidySQL(*before), tidySQL(*after)
		for _, s := range []string{b, a} {
			if err := val.Validate(s); err != nil {
				return fail(fmt.Errorf("a statement is not accepted: %w", err))
			}
		}
		if len(binds) > 0 {
			var err error
			if b, err = queryrunner.SubstituteParams(b, binds); err != nil {
				return fail(err)
			}
			if a, err = queryrunner.SubstituteParams(a, binds); err != nil {
				return fail(err)
			}
		}
		rep := &Report{SQL: b, InvestigationID: *invID}
		record := !*noRecord
		if record && rep.InvestigationID == 0 {
			id, err := be.RecordInvestigation(ctx, b, *title, nil)
			if err != nil {
				return fail(err)
			}
			rep.InvestigationID = id
		}
		cand := &CandidateResult{Kind: queryrunner.CandidateKindSQLRewrite, SQL: a, Category: "your rewrite", Confidence: "n/a",
			Rationale: "supplied with --after", Verdict: VerdictUnverified}
		if placeholderRe.MatchString(b) || placeholderRe.MatchString(a) {
			cand.Reason = "not measured: a statement has $n placeholders (pass --bind)"
		} else {
			proveCandidate(ctx, be, rep, cand, b, record)
		}
		rep.Candidates = []CandidateResult{*cand}
		if cand.Verdict == VerdictProven {
			rep.Proven = 1
		}
		if c.asJSON {
			printJSON(stdout, rep)
		} else {
			fmt.Fprintf(stdout, "Verdict: %s\n  %s\n", cand.Verdict, cand.Reason)
			if cand.BeforeMs > 0 || cand.AfterMs > 0 {
				fmt.Fprintf(stdout, "  rows %d, before %s, after %s, %.1fx\n", cand.Rows, ms(cand.BeforeMs), ms(cand.AfterMs), cand.Speedup)
			}
			if cand.ProofSource != "" {
				fmt.Fprintf(stdout, "  (%s)\n", cand.ProofSource)
			}
			if rep.InvestigationID > 0 {
				fmt.Fprintf(stdout, "  Evidence: pqn evidence %d\n", rep.InvestigationID)
			}
		}
		return rep.ExitCode()

	case "investigations":
		rows, err := be.Investigations(ctx, *n)
		if err != nil {
			return fail(err)
		}
		if c.asJSON {
			return printJSON(stdout, rows)
		}
		out := make([][]string, 0, len(rows))
		for _, r := range rows {
			out = append(out, []string{strconv.FormatInt(r.ID, 10), r.CreatedAt.Format("2006-01-02 15:04"), r.Who,
				strconv.FormatInt(r.Evidence, 10), r.Title, r.SQL})
		}
		Table(stdout, []string{"id", "when", "who", "evidence", "title", "statement"}, out, 60)
		return 0

	case "evidence":
		id := *invID
		if id == 0 && len(positional) == 1 {
			id, _ = strconv.ParseInt(positional[0], 10, 64)
		}
		if id == 0 {
			return fail(errors.New("give an investigation id: pqn evidence 7"))
		}
		rows, err := be.Evidence(ctx, id)
		if err != nil {
			return fail(err)
		}
		if c.asJSON {
			return printJSON(stdout, rows)
		}
		if len(rows) == 0 {
			fmt.Fprintln(stdout, "No evidence, or it belongs to someone else.")
		}
		for _, r := range rows {
			fmt.Fprintf(stdout, "#%d %s by %s at %s\n", r.ID, r.Kind, r.Who, r.CreatedAt.Format("2006-01-02 15:04:05"))
			fmt.Fprintf(stdout, "    %s\n", clip(string(r.Payload), 300))
		}
		return 0
	}
	return 1
}

func printJSON(w io.Writer, v any) int {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return 1
	}
	return 0
}

func cell(v any) string {
	switch t := v.(type) {
	case nil:
		return "NULL"
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}

func renderDoctor(w io.Writer, rows []DoctorRow) int {
	blocks, warns := 0, 0
	out := make([][]string, 0, len(rows))
	for _, r := range rows {
		switch r.Level {
		case "BLOCK":
			blocks++
		case "WARN":
			warns++
		}
		out = append(out, []string{r.Level, r.Check, r.Detail})
	}
	Table(w, []string{"level", "check", "detail"}, out, 100)
	for _, r := range rows {
		if (r.Level == "BLOCK" || r.Level == "WARN") && r.Fix != "" {
			fmt.Fprintf(w, "\nfix (%s): %s", r.Check, r.Fix)
		}
	}
	fmt.Fprintf(w, "\n\n%d blocking, %d warning(s).\n", blocks, warns)
	if blocks > 0 {
		return 1
	}
	return 0
}
