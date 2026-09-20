package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// realFacts loads the facts from the actual repository.
func realFacts(t *testing.T) pqnFacts {
	t.Helper()
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	r := &report{}
	_, sqlText := readPqnSQL(root, r)
	f := pqnFacts{
		tokens:  validPqnTokens(sqlText),
		funcs:   pqnFunctions(sqlText),
		targets: makeTargets(mustRead(root, "Makefile", r)),
		checks:  pqnCheckNames(sqlText),
		source:  collapse(sqlText + "\n" + readAllGo(root, "internal/pqncli")),
		root:    root,
	}
	f.commands, f.flags = pqnCLI(mustRead(root, "internal/pqncli/cli.go", r))
	if len(r.failures) > 0 {
		t.Fatalf("could not load facts: %v", r.failures)
	}
	return f
}

func TestPqnFactsAreReadFromTheCode(t *testing.T) {
	f := realFacts(t)
	for _, fn := range []string{"plan", "run", "top", "prove", "investigate", "measure_pair", "verify_setup", "expose", "enroll"} {
		if !f.funcs[fn] {
			t.Errorf("function %s should be found in the extension SQL", fn)
		}
	}
	for _, c := range []string{"doctor", "top", "plan", "run", "investigate", "prove", "investigations", "evidence", "version", "help"} {
		if !f.commands[c] {
			t.Errorf("command %s should be found in the tool", c)
		}
	}
	for _, fl := range []string{"dsn", "replica", "json", "bind", "queryid", "no-record", "max-proofs", "before", "after"} {
		if !f.flags[fl] {
			t.Errorf("flag --%s should be found in the tool", fl)
		}
	}
	for _, tok := range []string{"pqn_owner", "pqn_reader", "pqn_stats", "pqn_ledger", "pqn_viewer", "pqn_analyst", "pqn_admin", "pqn_api", "pqn_installer"} {
		if !f.tokens[tok] {
			t.Errorf("%s should be a valid role or schema name", tok)
		}
	}
	if !f.checks["login can become an owner role"] {
		t.Errorf("verify_setup check names should be found")
	}
}

func TestAPageThatDisagreesWithTheCodeFails(t *testing.T) {
	f := realFacts(t)
	cases := map[string]string{
		"a role that does not exist":  "Grant `pqn_superadmin` to alice.",
		"a function that is missing":  "Call `pqn_api.optimize_everything()` now.",
		"a make target that is gone":  "```shell\nmake install-pqn-magic\n```",
		"a command the tool lacks":    "```shell\npqn optimize --sql \"SELECT 1\"\n```",
		"a flag the tool lacks":       "```shell\npqn investigate --auto-apply --sql \"SELECT 1\"\n```",
		"a flag in an inline command": "Run `pqn top --sort mean`.",
		"a path that is not there":    "See `tools/docker/no-such-image.Dockerfile`.",
	}
	for name, page := range cases {
		r := &report{}
		checkPqnPage("x.md", page, f, r)
		if len(r.failures) == 0 {
			t.Errorf("%s: the check should have failed for %q", name, page)
		}
	}
}

func TestAPageThatAgreesWithTheCodePasses(t *testing.T) {
	f := realFacts(t)
	page := "Run `pqn top -n 5`, then:\n\n```bash\npqn investigate --queryid=1 --bind day --title \"x\"\nmake build-pqn\npsql -c \"SELECT pqn_api.expose_sql('a.b', ARRAY['c'])\"\n```\n\n```text\npqn dev\nGRANT pqn_analyst TO alice;\n```\n"
	r := &report{}
	checkPqnPage("x.md", page, f, r)
	if len(r.failures) != 0 {
		t.Errorf("a correct page should pass: %v", r.failures)
	}
}

func TestMessageInSource(t *testing.T) {
	source := collapse("RAISE EXCEPTION 'pqn: role % does not exist. Run pqn-roles.sql first.'; RAISE EXCEPTION 'pqn: run pqn_api.init() first'; fmt.Errorf(\"connect to the primary: %w\")")
	good := []string{
		"pqn: role pqn_owner does not exist. Run pqn-roles.sql first.",
		"pqn: run pqn_api.init() first",
		"pqn: connect to the primary: ...",
	}
	for _, m := range good {
		if !messageInSource(m, source) {
			t.Errorf("%q should be found", m)
		}
	}
	bad := []string{"pqn: the frobnicator is not calibrated", "pqn: role pqn_owner does not exist. Run the magic script first."}
	for _, m := range bad {
		if messageInSource(m, source) {
			t.Errorf("%q should not be found", m)
		}
	}
}

func TestWarningNamesAndMessagesAreReadFromTheTables(t *testing.T) {
	body := "Warnings you should expect on a normal setup:\n\n| Warning | Why | Action |\n|---|---|---|\n| `table exposed with scope full` | x | y |\n| `pg_stat_statements` | x | y |\n\nText.\n\n## Troubleshooting installation and setup\n\n| Message | Cause | Fix |\n|---|---|---|\n| `pqn: run pqn_api.init() first` | a | b |\n| `permission denied for schema pqn_api` | a | b |\n"
	if got := warningNamesInTable(body); strings.Join(got, ",") != "table exposed with scope full,pg_stat_statements" {
		t.Errorf("warning names = %v", got)
	}
	if got := troubleshootingMessages(body); len(got) != 1 || got[0] != "pqn: run pqn_api.init() first" {
		t.Errorf("messages = %v (PostgreSQL's own messages are not checked)", got)
	}
}

func TestTheRealPqnPagesPass(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "docs/getting-started/pqn-installation.md")); err != nil {
		t.Skip("pqn docs not present")
	}
	r := &report{}
	checkPqnDocs(root, r)
	if len(r.failures) != 0 {
		t.Errorf("the pqn docs disagree with the code: %v", r.failures)
	}
}

// The reference page must be complete: leaving out any function, command, flag, environment variable
// or table the code defines is a failure, and helper functions are exempt.
func TestPqnReferenceMustBeComplete(t *testing.T) {
	f := pqnFacts{
		funcs:    map[string]bool{"run": true, "prove": true, "explain_ms": true},
		commands: map[string]bool{"top": true, "prove": true, "help": true},
		flags:    map[string]bool{"json": true, "n": true, "help": true},
	}
	sql := "CREATE TABLE IF NOT EXISTS pqn.limits (x int);\nCREATE TABLE pqn_ledger.evidence (x int);"
	cli := `fs.StringVar(&c.dsn, "dsn", getenv("PQN_DSN"), "x")`
	complete := "`pqn_api.run` `pqn_api.prove` `top` `prove` `--json` `-n` PQN_DSN pqn.limits pqn_ledger.evidence"

	r := &report{}
	checkPqnReference(complete, f, sql, cli, r)
	if len(r.failures) != 0 {
		t.Fatalf("a complete page failed: %v", r.failures)
	}

	for name, page := range map[string]string{
		"a function":   strings.Replace(complete, "`pqn_api.prove`", "", 1),
		"a command":    strings.Replace(complete, " `top` ", " ", 1),
		"a flag":       strings.Replace(complete, "`--json`", "", 1),
		"a short flag": strings.Replace(complete, "`-n`", "", 1),
		"an env var":   strings.Replace(complete, "PQN_DSN", "", 1),
		"a table":      strings.Replace(complete, "pqn.limits", "", 1),
	} {
		r := &report{}
		checkPqnReference(page, f, sql, cli, r)
		if len(r.failures) != 1 {
			t.Errorf("leaving out %s: got %d failures %v, want exactly 1", name, len(r.failures), r.failures)
		}
	}

	// A helper needs no entry, and an empty page is left to the file check.
	r = &report{}
	checkPqnReference(complete, f, sql, cli, r)
	for _, msg := range r.failures {
		if strings.Contains(msg, "explain_ms") {
			t.Errorf("a helper function was demanded: %s", msg)
		}
	}
	r = &report{}
	checkPqnReference("", f, sql, cli, r)
	if len(r.failures) != 0 {
		t.Errorf("an empty page must be skipped, got %v", r.failures)
	}
}
