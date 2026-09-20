package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// The pqn documentation states things the extension and the tool decide: the files to install, the
// version, the role and function names, the commands and flags, the warnings the setup check
// prints, and the messages a failed installation shows. These checks fail when a page disagrees
// with the code. They are narrow on purpose: whether the steps *work* is what
// `make verify-pqn-docs` proves by running them.

// pqnDocPages are the pages checked. Each is a file under docs/.
var pqnDocPages = []string{
	"docs/getting-started/pqn-extension.md",
	"docs/getting-started/pqn-installation.md",
	pqnReferencePage,
}

// pqnReferencePage documents every function, command, flag and environment variable. The other pages
// are checked for what they name; this one is also checked for what it leaves out.
const pqnReferencePage = "docs/reference/pqn.md"

// pqnInternalFuncs are helpers only their owner can execute. They need no reference entry.
var pqnInternalFuncs = map[string]bool{"explain_ms": true, "exposed_path": true}

const pqnExtDir = "infra/pqn-extension"

var (
	pqnControlVersionRE = regexp.MustCompile(`(?m)^default_version\s*=\s*'([^']+)'`)
	pqnTokenRE          = regexp.MustCompile(`\bpqn_[a-z_]+\b`)
	pqnFuncCallRE       = regexp.MustCompile(`\bpqn_api\.([a-z_]+)\b`)
	pqnCreateFuncRE     = regexp.MustCompile(`(?im)^CREATE(?: OR REPLACE)? FUNCTION pqn_api\.([a-z_]+)\(`)
	pqnRepoPathRE       = regexp.MustCompile("(?:^|[\\s(/`\"])((?:tools|infra|cmd|internal)/[A-Za-z0-9_./-]*[A-Za-z0-9_])")
	pqnMakeTargetRE     = regexp.MustCompile(`\bmake ([a-z][a-z0-9-]*)`)
	pqnCommandRE        = regexp.MustCompile("(?m)(?:^|[`$|]\\s*)pqn ([a-z][a-z-]*)")
	pqnFlagRE           = regexp.MustCompile(`\s--([a-z][a-z-]*)`)
	pqnCheckNameRE      = regexp.MustCompile(`check_name := '([^']+)'`)
	pqnCaseRE           = regexp.MustCompile(`case ((?:"[a-z]+",? ?)+):`)
	pqnFlagDeclRE       = regexp.MustCompile(`fs\.\w+\((?:&\w+(?:\.\w+)?, )?"([a-z][a-z-]*)"`)
	fenceRE             = regexp.MustCompile("(?s)```([a-z]*)\n(.*?)```")
	codeSpanRE          = regexp.MustCompile("`([^`\n]+)`")
	messageSplitRE      = regexp.MustCompile(`<[^>]+>|pqn_[a-z_]+|\d+|\.\.\.`)
)

// checkPqnDocs runs every check on the pqn pages.
func checkPqnDocs(root string, r *report) {
	sqlFiles, sqlText := readPqnSQL(root, r)
	if sqlText == "" {
		return
	}
	goText := readAllGo(root, "internal/pqncli")
	makefile := mustRead(root, "Makefile", r)
	cliSrc := mustRead(root, "internal/pqncli/cli.go", r)

	pages := map[string]string{}
	for _, rel := range pqnDocPages {
		pages[rel] = mustRead(root, rel, r)
	}

	checkPqnFiles(root, pages, sqlFiles, r)
	facts := pqnFacts{
		tokens:  validPqnTokens(sqlText),
		funcs:   pqnFunctions(sqlText),
		targets: makeTargets(makefile),
		checks:  pqnCheckNames(sqlText),
		source:  collapse(sqlText + "\n" + goText),
		root:    root,
	}
	facts.commands, facts.flags = pqnCLI(cliSrc)
	for rel, body := range pages {
		checkPqnPage(rel, body, facts, r)
	}

	install := pages["docs/getting-started/pqn-installation.md"]
	for _, name := range warningNamesInTable(install) {
		if !facts.checks[name] {
			r.failf("pqn-installation.md: lists the setup warning %q, which verify_setup() never reports", name)
		}
	}
	for _, msg := range troubleshootingMessages(install) {
		if !messageInSource(msg, facts.source) {
			r.failf("pqn-installation.md: troubleshooting shows the message %q, which no pqn source produces", msg)
		}
	}
	checkPqnVersion(install, sqlFiles, r)
	checkPqnReference(pages[pqnReferencePage], facts, sqlText, cliSrc, r)
}

var (
	pqnCreateTableRE = regexp.MustCompile(`(?im)^\s*CREATE TABLE (?:IF NOT EXISTS )?(pqn(?:_ledger)?\.[a-z_]+)`)
	pqnGetenvRE      = regexp.MustCompile(`getenv\("([A-Z_]+)"\)`)
)

// checkPqnReference fails when the reference page leaves out something the code defines: a function,
// a command, a flag, an environment variable, or a table the extension creates.
func checkPqnReference(body string, f pqnFacts, sqlText, cliSrc string, r *report) {
	const rel = pqnReferencePage
	if body == "" {
		return
	}
	for _, fn := range sortedKeys(f.funcs) {
		if pqnInternalFuncs[fn] {
			continue
		}
		if !strings.Contains(body, "pqn_api."+fn) {
			r.failf("%s: does not document pqn_api.%s", rel, fn)
		}
	}
	for _, c := range sortedKeys(f.commands) {
		if c == "help" {
			continue
		}
		if !strings.Contains(body, "`"+c) && !strings.Contains(body, "pqn "+c) {
			r.failf("%s: does not document the command %q", rel, c)
		}
	}
	for _, fl := range sortedKeys(f.flags) {
		if fl == "help" {
			continue
		}
		if !strings.Contains(body, "`--"+fl+"`") && !(len(fl) == 1 && strings.Contains(body, "`-"+fl+"`")) {
			r.failf("%s: does not document the flag %q", rel, fl)
		}
	}
	for _, m := range pqnGetenvRE.FindAllStringSubmatch(cliSrc, -1) {
		if !strings.Contains(body, m[1]) {
			r.failf("%s: does not mention the environment variable %s", rel, m[1])
		}
	}
	for _, m := range pqnCreateTableRE.FindAllStringSubmatch(sqlText, -1) {
		if !strings.Contains(body, m[1]) {
			r.failf("%s: does not document the table %s", rel, m[1])
		}
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// pqnFacts is what the code decides, gathered once.
type pqnFacts struct {
	tokens   map[string]bool // roles and schemas in the extension's SQL
	funcs    map[string]bool // functions in schema pqn_api
	targets  map[string]bool // Makefile targets
	commands map[string]bool // pqn commands
	flags    map[string]bool // pqn flags
	checks   map[string]bool // verify_setup() check names
	source   string          // SQL and Go source, whitespace collapsed
	root     string          // repository root, for paths the pages name
}

// checkPqnPage checks one page against the facts.
func checkPqnPage(rel, body string, f pqnFacts, r *report) {
	for _, tok := range uniq(pqnTokenRE.FindAllString(body, -1)) {
		if !f.tokens[tok] {
			r.failf("%s: mentions %q, which is not a role or schema in %s", rel, tok, pqnExtDir)
		}
	}
	for _, m := range pqnFuncCallRE.FindAllStringSubmatch(body, -1) {
		if !f.funcs[m[1]] {
			r.failf("%s: calls pqn_api.%s, which the extension does not define", rel, m[1])
		}
	}
	if f.root != "" {
		for _, path := range uniq(matches1(pqnRepoPathRE, body)) {
			if _, err := os.Stat(filepath.Join(f.root, path)); err != nil {
				r.failf("%s: names the path %s, which does not exist", rel, path)
			}
		}
	}
	code := codeText(body)
	for _, t := range uniq(matches1(pqnMakeTargetRE, code)) {
		if !f.targets[t] {
			r.failf("%s: says `make %s`, which the Makefile does not define", rel, t)
		}
	}
	for _, c := range uniq(matches1(pqnCommandRE, code)) {
		if !f.commands[c] {
			r.failf("%s: shows `pqn %s`, which the tool does not have", rel, c)
		}
	}
	for _, line := range strings.Split(code, "\n") {
		if !strings.Contains(line, "pqn ") {
			continue
		}
		for _, fl := range matches1(pqnFlagRE, line) {
			if !f.flags[fl] {
				r.failf("%s: shows the flag --%s on a pqn command, which the tool does not define", rel, fl)
			}
		}
	}
}

// readPqnSQL returns the extension's SQL files and their text joined.
func readPqnSQL(root string, r *report) (files []string, text string) {
	ents, err := os.ReadDir(filepath.Join(root, pqnExtDir))
	if err != nil {
		r.failf("cannot read %s: %v", pqnExtDir, err)
		return nil, ""
	}
	var b strings.Builder
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		files = append(files, e.Name())
		b.WriteString(mustRead(root, filepath.Join(pqnExtDir, e.Name()), r))
		b.WriteString("\n")
	}
	return files, b.String()
}

func readAllGo(root, dir string) string {
	var b strings.Builder
	_ = filepath.WalkDir(filepath.Join(root, dir), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if body, err := os.ReadFile(path); err == nil {
			b.Write(body)
			b.WriteString("\n")
		}
		return nil
	})
	return b.String()
}

// checkPqnFiles: every extension file the installation guide names exists, and every version
// script in the directory is named.
func checkPqnFiles(root string, pages map[string]string, sqlFiles []string, r *report) {
	install := pages["docs/getting-started/pqn-installation.md"]
	named := map[string]bool{"pqn.control": true}
	for _, f := range sqlFiles {
		named[f] = true
	}
	for _, m := range regexp.MustCompile("`(pqn[-a-z0-9.]*\\.(?:sql|control))`").FindAllStringSubmatch(install, -1) {
		if !named[m[1]] {
			r.failf("pqn-installation.md: names the file %s, which is not in %s", m[1], pqnExtDir)
		}
	}
	for _, f := range append([]string{"pqn.control"}, sqlFiles...) {
		if !strings.Contains(install, f) {
			r.failf("pqn-installation.md: does not mention %s, which is in %s", f, pqnExtDir)
		}
	}
	for _, mk := range []string{"install-pqn-extension", "build-pqn", "build-pqn-image"} {
		if !strings.Contains(install, "make "+mk) {
			r.failf("pqn-installation.md: does not mention `make %s`", mk)
		}
	}
	if !strings.Contains(mustRead(root, "tools/db/install-pqn-extension.sh", r), "pqn--*.sql") {
		r.failf("tools/db/install-pqn-extension.sh: must copy every pqn--*.sql, or an upgrade path is missed")
	}
}

// checkPqnVersion: the guide states the control file's default_version.
func checkPqnVersion(install string, sqlFiles []string, r *report) {
	// The control file is read by the caller through mustRead in readPqnSQL's directory; read it here.
	root, err := repoRoot()
	if err != nil {
		return
	}
	control := mustRead(root, filepath.Join(pqnExtDir, "pqn.control"), r)
	m := pqnControlVersionRE.FindStringSubmatch(control)
	if m == nil {
		r.failf("%s/pqn.control: no default_version", pqnExtDir)
		return
	}
	if !strings.Contains(install, "`default_version` is **"+m[1]+"**") {
		r.failf("pqn-installation.md: must say `default_version` is **%s**, as pqn.control does", m[1])
	}
	if !strings.Contains(install, "pqn "+m[1]) {
		r.failf("pqn-installation.md: the expected output must show `pqn %s`", m[1])
	}
	var hasStep bool
	for _, f := range sqlFiles {
		if strings.HasSuffix(f, "--"+m[1]+".sql") {
			hasStep = true
		}
	}
	if !hasStep {
		r.failf("%s: no upgrade script ends at %s, the default_version", pqnExtDir, m[1])
	}
}

func validPqnTokens(sqlText string) map[string]bool {
	valid := map[string]bool{"pqn_installer": true}
	for _, t := range pqnTokenRE.FindAllString(sqlText, -1) {
		valid[t] = true
	}
	return valid
}

func pqnFunctions(sqlText string) map[string]bool {
	out := map[string]bool{}
	for _, m := range pqnCreateFuncRE.FindAllStringSubmatch(sqlText, -1) {
		out[m[1]] = true
	}
	return out
}

func makeTargets(makefile string) map[string]bool {
	out := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^([a-z][a-z0-9-]*):`).FindAllStringSubmatch(makefile, -1) {
		out[m[1]] = true
	}
	return out
}

// pqnCLI reads the commands and flags the tool defines from its source.
func pqnCLI(src string) (commands, flags map[string]bool) {
	commands = map[string]bool{"help": true, "version": true}
	for _, m := range pqnCaseRE.FindAllStringSubmatch(src, -1) {
		for _, c := range regexp.MustCompile(`"([a-z]+)"`).FindAllStringSubmatch(m[1], -1) {
			commands[c[1]] = true
		}
	}
	flags = map[string]bool{"help": true}
	for _, m := range pqnFlagDeclRE.FindAllStringSubmatch(src, -1) {
		flags[m[1]] = true
	}
	return commands, flags
}

func pqnCheckNames(sqlText string) map[string]bool {
	out := map[string]bool{}
	for _, m := range pqnCheckNameRE.FindAllStringSubmatch(sqlText, -1) {
		out[m[1]] = true
	}
	return out
}

// codeText is the text of every fenced block that a reader would run (bash, shell, sql, dockerfile),
// and every inline code span. Output blocks (text) are left out, since they hold program output.
func codeText(body string) string {
	var b strings.Builder
	for _, m := range fenceRE.FindAllStringSubmatch(body, -1) {
		switch m[1] {
		case "bash", "shell", "sh", "sql", "dockerfile":
			b.WriteString(m[2])
			b.WriteString("\n")
		}
	}
	stripped := fenceRE.ReplaceAllString(body, "")
	for _, m := range codeSpanRE.FindAllStringSubmatch(stripped, -1) {
		b.WriteString("`" + m[1] + "`\n")
	}
	return b.String()
}

// warningNamesInTable returns the first column of the "Warnings you should expect" table.
func warningNamesInTable(body string) []string {
	i := strings.Index(body, "Warnings you should expect")
	if i < 0 {
		return nil
	}
	var names []string
	for _, line := range strings.Split(body[i:], "\n")[1:] {
		line = strings.TrimSpace(line)
		if line == "" && len(names) > 0 {
			break
		}
		if !strings.HasPrefix(line, "| `") {
			continue
		}
		cells := strings.Split(line, "|")
		if len(cells) < 2 {
			continue
		}
		name := strings.Trim(strings.TrimSpace(cells[1]), "`")
		names = append(names, name)
	}
	return names
}

// troubleshootingMessages returns the messages, in the first column of the troubleshooting table,
// that start with pqn: . PostgreSQL's own messages are not checked.
func troubleshootingMessages(body string) []string {
	i := strings.Index(body, "## Troubleshooting installation and setup")
	if i < 0 {
		return nil
	}
	var out []string
	for _, line := range strings.Split(body[i:], "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "| `pqn:") {
			continue
		}
		cells := strings.Split(line, "|")
		msg := strings.TrimSpace(cells[1])
		msg = strings.TrimPrefix(msg, "`")
		msg = strings.TrimSuffix(msg, "`")
		out = append(out, msg)
	}
	return out
}

// messageInSource reports whether a message a person sees can be produced by the source. Names and
// numbers in the message vary, so the message is cut at those, and each remaining piece of 12
// characters or more must appear. It tries the message with and without the "pqn: " prefix, which
// the tool adds to errors it prints.
func messageInSource(msg, source string) bool {
	for _, m := range []string{msg, strings.TrimPrefix(msg, "pqn: ")} {
		ok := true
		for _, piece := range messageSplitRE.Split(m, -1) {
			piece = collapse(piece)
			if len(piece) < 12 {
				continue
			}
			if !strings.Contains(source, piece) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func collapse(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "%s", "%")), " ")
}

func matches1(re *regexp.Regexp, s string) []string {
	var out []string
	for _, m := range re.FindAllStringSubmatch(s, -1) {
		out = append(out, m[1])
	}
	return out
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
