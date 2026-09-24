// Command docscheck fails when the documentation states something the code
// decides. It is deliberately narrow: it checks the facts most prone to silent
// drift (Go version, Compose Postgres default, configuration variables and a few
// critical defaults, release platforms, the API surface, error codes), forbidden
// stale vocabulary, and the relative links/anchors in the Markdown that MkDocs
// does not build. Anything subtler is left to review.
//
// Run from the repo root:  go run ./tools/docscheck
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// report accumulates failures; a non-empty list means exit 1.
type report struct{ failures []string }

func (r *report) failf(format string, a ...any) {
	r.failures = append(r.failures, fmt.Sprintf(format, a...))
}

func main() {
	root, err := repoRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, "docscheck: "+err.Error())
		os.Exit(2)
	}
	r := &report{}

	checkGoVersion(root, r)
	checkComposePostgres(root, r)
	cfgVars := checkConfigVars(root, r)
	checkCriticalConfigDefaults(root, r)
	checkReleasePlatforms(root, r)
	checkAPICoverage(root, r)
	checkErrorCodes(root, r)
	checkForbiddenVocabulary(root, r)
	checkRepoMarkdownLinks(root, r)
	checkNavCoverage(root, r)
	checkPqnDocs(root, r)
	_ = cfgVars

	if len(r.failures) == 0 {
		fmt.Println("docscheck: OK")
		return
	}
	sort.Strings(r.failures)
	fmt.Fprintf(os.Stderr, "docscheck: %d problem(s):\n", len(r.failures))
	for _, f := range r.failures {
		fmt.Fprintln(os.Stderr, "  - "+f)
	}
	os.Exit(1)
}

func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not find go.mod above %s", dir)
		}
		dir = parent
	}
}

func mustRead(root, rel string, r *report) string {
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		r.failf("cannot read %s: %v", rel, err)
		return ""
	}
	return string(b)
}

// --- Go version -------------------------------------------------------------

var goModVersionRE = regexp.MustCompile(`(?m)^go[ \t]+(\d+)\.(\d+)`)
var goMentionRE = regexp.MustCompile(`Go[ \-](\d+)\.(\d+)`)

func checkGoVersion(root string, r *report) {
	gm := mustRead(root, "go.mod", r)
	m := goModVersionRE.FindStringSubmatch(gm)
	if m == nil {
		r.failf("go.mod: no `go X.Y` directive found")
		return
	}
	want := m[1] + "." + m[2]
	for _, rel := range []string{"README.md", "docs/reference/versions-limits.md", "docs/development/setup.md", "docs/getting-started/installation.md", "docs/getting-started/quickstart.md"} {
		body := mustRead(root, rel, r)
		for _, mm := range goMentionRE.FindAllStringSubmatch(body, -1) {
			got := mm[1] + "." + mm[2]
			if got != want {
				r.failf("%s: mentions %q but go.mod requires Go %s", rel, mm[0], want)
			}
		}
	}
}

// --- Compose Postgres default --------------------------------------------------

var composeImageRE = regexp.MustCompile(`POSTGRES_IMAGE:-([a-z0-9./:-]+)}`)

func checkComposePostgres(root string, r *report) {
	compose := mustRead(root, "docker-compose.yml", r)
	m := composeImageRE.FindStringSubmatch(compose)
	if m == nil {
		r.failf("docker-compose.yml: could not find POSTGRES_IMAGE default")
		return
	}
	def := m[1] // e.g. postgres:16-alpine
	cfg := mustRead(root, "docs/reference/configuration.md", r)
	// The POSTGRES_IMAGE row must carry the real default.
	row := findTableRow(cfg, "POSTGRES_IMAGE")
	if row == "" {
		r.failf("docs/reference/configuration.md: no POSTGRES_IMAGE row")
	} else if !strings.Contains(row, def) {
		r.failf("docs/reference/configuration.md: POSTGRES_IMAGE row does not mention the compose default %q", def)
	}
}

// --- Configuration variables ------------------------------------------------

var getEnvNameRE = regexp.MustCompile(`getEnv\w*\(\s*"([A-Z][A-Z0-9_]+)"`)
var osGetenvNameRE = regexp.MustCompile(`os\.Getenv\(\s*"([A-Z][A-Z0-9_]+)"`)

// Variables read outside config.Load() (entrypoint, principal, debuglog, mode).
var extraConfigVars = []string{
	"APP_ENV", "SECURITY_STRICT", "LOG_DEBUG",
	"DATABASE_MIGRATION_USER", "DATABASE_MIGRATION_PASSWORD", "DATABASE_MIGRATION_URL",
	"PGQUERYNARRATIVE_SKIP_MIGRATIONS", "PGQUERYNARRATIVE_SEED",
}

// Documented in configuration.md but not read by the Go config loader (compose /
// CLI / MCP owned). These are allowed to appear as formal rows.
var allowedForeignConfigVars = map[string]bool{
	"POSTGRES_IMAGE": true,
}

// Code vars that are internal enough not to require a formal doc row.
var configVarsDocExempt = map[string]bool{
	"DEFAULT_ORGANIZATION_ID": true,
}

func checkConfigVars(root string, r *report) []string {
	src := mustRead(root, "app/config/config.go", r)
	seen := map[string]bool{}
	for _, m := range getEnvNameRE.FindAllStringSubmatch(src, -1) {
		seen[m[1]] = true
	}
	for _, m := range osGetenvNameRE.FindAllStringSubmatch(src, -1) {
		seen[m[1]] = true
	}
	for _, v := range extraConfigVars {
		seen[v] = true
	}
	if len(seen) < 40 {
		r.failf("app/config/config.go: only %d env vars parsed — extraction regex likely broke", len(seen))
		return nil
	}

	doc := mustRead(root, "docs/reference/configuration.md", r)
	// Every code variable must be mentioned somewhere in the reference.
	var names []string
	for v := range seen {
		names = append(names, v)
	}
	sort.Strings(names)
	for _, v := range names {
		if configVarsDocExempt[v] {
			continue
		}
		if !strings.Contains(doc, "`"+v+"`") {
			r.failf("docs/reference/configuration.md: missing configuration variable %s (read in the code)", v)
		}
	}

	// Every formal row (| `VAR` | ...) must be a real variable.
	rowVarRE := regexp.MustCompile(`(?m)^\|\s*` + "`" + `([A-Z][A-Z0-9_]+)` + "`" + `[^|]*\|`)
	for _, m := range rowVarRE.FindAllStringSubmatch(doc, -1) {
		v := m[1]
		if seen[v] || allowedForeignConfigVars[v] {
			continue
		}
		// Split "A / B" style rows: accept if any half is known.
		r.failf("docs/reference/configuration.md: documents unknown configuration variable %s", v)
	}
	return names
}

// A few high-value defaults checked exactly (name -> substring that must appear
// in that variable's reference row).
var criticalDefaults = map[string]string{
	"SECURITY_OIDC_AUTO_JOIN_DEFAULT_ORG":      "`false`",
	"REGRESSION_POLLER_INTERVAL":               "15m",
	"SECURITY_EXPLAIN_SNAPSHOT_RETENTION_DAYS": "90",
	"DATABASE_GLOBAL_MAX_CONNECTIONS":          "0",
	"LLM_MAX_SAMPLE_ROWS":                      "5",
	"QUERY_TIMEOUT":                            "30s",
	"SECURITY_SESSION_TTL":                     "8h",
}

func checkCriticalConfigDefaults(root string, r *report) {
	doc := mustRead(root, "docs/reference/configuration.md", r)
	names := make([]string, 0, len(criticalDefaults))
	for k := range criticalDefaults {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, v := range names {
		row := findTableRow(doc, v)
		if row == "" {
			r.failf("docs/reference/configuration.md: no row for %s (critical-default check)", v)
			continue
		}
		if !strings.Contains(row, criticalDefaults[v]) {
			r.failf("docs/reference/configuration.md: %s row is missing its documented default %q", v, criticalDefaults[v])
		}
	}
}

// --- Release platforms ----------------------------------------------------------

var goosRE = regexp.MustCompile(`goos:\s*(\w+)`)
var goarchRE = regexp.MustCompile(`goarch:\s*(\w+)`)

func checkReleasePlatforms(root string, r *report) {
	wf := mustRead(root, ".github/workflows/release.yml", r)
	goos := goosRE.FindAllStringSubmatch(wf, -1)
	goarch := goarchRE.FindAllStringSubmatch(wf, -1)
	if len(goos) == 0 || len(goos) != len(goarch) {
		r.failf(".github/workflows/release.yml: could not pair goos/goarch (%d/%d)", len(goos), len(goarch))
		return
	}
	var platforms []string
	for i := range goos {
		platforms = append(platforms, goos[i][1]+"/"+goarch[i][1])
	}
	for _, rel := range []string{"docs/project/releases.md", "docs/reference/versions-limits.md"} {
		body := mustRead(root, rel, r)
		for _, p := range platforms {
			slash := p                              // linux/amd64
			dash := strings.ReplaceAll(p, "/", "-") // linux-amd64
			if !strings.Contains(body, slash) && !strings.Contains(body, dash) {
				r.failf("%s: release platform %q from release.yml is not documented", rel, p)
			}
		}
	}
}

// --- API coverage -------------------------------------------------------------

func checkAPICoverage(root string, r *report) {
	raw := mustRead(root, "api/gen/http/openapi3.json", r)
	if raw == "" {
		return
	}
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		r.failf("api/gen/http/openapi3.json: %v", err)
		return
	}
	apiMD := mustRead(root, "docs/reference/api.md", r)
	for p := range doc.Paths {
		rel := strings.TrimPrefix(p, "/api/v1") // api.md documents paths relative to the base
		if rel == "" {
			rel = "/"
		}
		if !strings.Contains(apiMD, "`"+rel+"`") {
			r.failf("docs/reference/api.md: OpenAPI path %s (`%s`) is not in the API reference", p, rel)
		}
	}
}

// --- Error codes ------------------------------------------------------------

var strPtrCodeRE = regexp.MustCompile(`strPtr\("([A-Z][A-Z0-9_]+)"\)`)
var jsonCodeRE = regexp.MustCompile(`"code"\s*:\s*"([A-Z][A-Z0-9_]+)"`)

func checkErrorCodes(root string, r *report) {
	codes := map[string]bool{}
	roots := []string{"app", "cmd", "web"}
	for _, dir := range roots {
		_ = filepath.WalkDir(filepath.Join(root, dir), func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			b, _ := os.ReadFile(path)
			for _, m := range strPtrCodeRE.FindAllStringSubmatch(string(b), -1) {
				codes[m[1]] = true
			}
			for _, m := range jsonCodeRE.FindAllStringSubmatch(string(b), -1) {
				codes[m[1]] = true
			}
			return nil
		})
	}
	if len(codes) == 0 {
		r.failf("no structured error codes found in app/cmd/web — extraction likely broke")
		return
	}
	doc := mustRead(root, "docs/reference/api-errors.md", r)
	var list []string
	for c := range codes {
		list = append(list, c)
	}
	sort.Strings(list)
	for _, c := range list {
		if !strings.Contains(doc, "`"+c+"`") {
			r.failf("docs/reference/api-errors.md: error code %s is emitted by the code but not documented", c)
		}
	}
}

// --- Forbidden vocabulary ----------------------------------------------------

type vocabRule struct {
	name string
	re   *regexp.Regexp
}

var forbiddenVocab = []vocabRule{
	{"\"requires equivalence Equal\"", regexp.MustCompile(`requires equivalence \*{0,2}Equal`)},
	{"\"equivalence proof\"", regexp.MustCompile(`(?i)equivalence proof`)},
	{"bare `regression_id`", regexp.MustCompile(`\bregression_id\b`)},
	{"Go 1.20–1.25 claim", regexp.MustCompile(`Go[ \-]1\.2[0-5]\b`)},
	{"\"8,000 rows\" dataset claim", regexp.MustCompile(`\b8,?000[ \-]rows?\b`)},
	{"\"mathematically prove\" (verification is never a mathematical proof)", regexp.MustCompile(`(?i)mathematically\s+prov`)},
	{"em dash (—)", regexp.MustCompile("—")},
}

// The stale-owner-URL check is a plain substring match, not a regex: a regex
// shaped like a URL (`https?://host/...`) reads to a static scanner as a host
// check missing an anchor, since the same shape elsewhere might be used to
// validate a URL's origin. Here it only ever scans doc prose for one exact,
// fully-qualified string, so there is no partial-match or embedded-host risk
// to anchor against — `strings.Contains` says that directly instead of
// leaving a scanner to infer it from an unanchored regex.
var staleOwnerURLs = []string{
	"https://github.com/pgquerynarrative/",
	"http://github.com/pgquerynarrative/",
}

func checkForbiddenVocabulary(root string, r *report) {
	files := []string{"README.md", "RELEASING.md", "deploy/README.md", "examples/README.md"}
	for _, d := range []string{".github", "docs"} {
		_ = filepath.WalkDir(filepath.Join(root, d), func(path string, de os.DirEntry, err error) error {
			if err != nil || de.IsDir() || !strings.HasSuffix(path, ".md") {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			files = append(files, rel)
			return nil
		})
	}
	for _, rel := range files {
		if rel == "CHANGELOG.md" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			continue
		}
		body := string(b)
		for _, rule := range forbiddenVocab {
			if loc := rule.re.FindStringIndex(body); loc != nil {
				r.failf("%s: forbidden vocabulary %s near %q", rel, rule.name, snippet(body, loc[0]))
			}
		}
		for _, needle := range staleOwnerURLs {
			if i := strings.Index(body, needle); i >= 0 {
				r.failf("%s: forbidden vocabulary stale owner browser URL near %q", rel, snippet(body, i))
			}
		}
	}
}

func snippet(s string, at int) string {
	start := at - 20
	if start < 0 {
		start = 0
	}
	end := at + 40
	if end > len(s) {
		end = len(s)
	}
	return strings.ReplaceAll(s[start:end], "\n", " ")
}

// --- Relative links + anchors in repo Markdown ------------------------------

var mdLinkRE = regexp.MustCompile(`\[[^\]]*\]\(([^)]+)\)`)
var headingRE = regexp.MustCompile(`(?m)^#{1,6}\s+(.+?)\s*$`)
var explicitIDRE = regexp.MustCompile(`\{#([A-Za-z0-9_-]+)\}`)
var htmlAnchorRE = regexp.MustCompile(`<a\s+(?:name|id)="([A-Za-z0-9_-]+)"`)

// Markdown files not built by MkDocs — mkdocs --strict cannot check these.
var repoMarkdownFiles = []string{
	"README.md", "RELEASING.md", "deploy/README.md", "examples/README.md",
	".github/CONTRIBUTING.md", ".github/SECURITY.md", ".github/CODE_OF_CONDUCT.md",
}

func checkRepoMarkdownLinks(root string, r *report) {
	for _, rel := range repoMarkdownFiles {
		body := mustRead(root, rel, r)
		if body == "" {
			continue
		}
		dir := filepath.Dir(filepath.Join(root, rel))
		for _, m := range mdLinkRE.FindAllStringSubmatch(body, -1) {
			target := strings.TrimSpace(m[1])
			if i := strings.IndexAny(target, " \t"); i >= 0 {
				target = target[:i] // drop optional "title"
			}
			if target == "" || strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") ||
				strings.HasPrefix(target, "mailto:") || strings.HasPrefix(target, "tel:") {
				continue
			}
			path, anchor := target, ""
			if i := strings.Index(target, "#"); i >= 0 {
				path, anchor = target[:i], target[i+1:]
			}
			var resolved string
			if path == "" {
				resolved = filepath.Join(root, rel) // same-file anchor
			} else {
				resolved = filepath.Clean(filepath.Join(dir, path))
				if _, err := os.Stat(resolved); err != nil {
					r.failf("%s: link target %q does not exist", rel, target)
					continue
				}
			}
			if anchor == "" {
				continue
			}
			tb, err := os.ReadFile(resolved)
			if err != nil {
				continue // not a markdown file we can introspect
			}
			if !strings.HasSuffix(resolved, ".md") {
				continue
			}
			if !anchorExists(string(tb), anchor) {
				r.failf("%s: link %q points at a missing anchor", rel, target)
			}
		}
	}
}

func anchorExists(body, anchor string) bool {
	want := strings.ToLower(anchor)
	for _, m := range explicitIDRE.FindAllStringSubmatch(body, -1) {
		if strings.ToLower(m[1]) == want {
			return true
		}
	}
	for _, m := range htmlAnchorRE.FindAllStringSubmatch(body, -1) {
		if strings.ToLower(m[1]) == want {
			return true
		}
	}
	for _, m := range headingRE.FindAllStringSubmatch(body, -1) {
		if slugify(m[1]) == want {
			return true
		}
	}
	return false
}

var slugStripRE = regexp.MustCompile(`[^\w\- ]+`)

// slugify mirrors GitHub's heading-anchor algorithm (these repo-root Markdown
// files render on GitHub, not through MkDocs): lowercase, drop punctuation
// except `-`/`_`, spaces to `-`, and — unlike MkDocs — consecutive hyphens are
// NOT collapsed.
func slugify(h string) string {
	h = explicitIDRE.ReplaceAllString(h, "")
	h = strings.ToLower(strings.TrimSpace(h))
	h = strings.ReplaceAll(h, "`", "")
	h = slugStripRE.ReplaceAllString(h, "")
	h = strings.ReplaceAll(h, " ", "-")
	return strings.Trim(h, "-")
}

// --- MkDocs nav coverage ---------------------------------------------------

// A nav entry is "Title: path.md" — capture only the value, so a label that
// happens to contain ".md" and an external "Title: https://….md" URL are ignored.
var navValueRE = regexp.MustCompile(`(?m):[ \t]*(\S+)[ \t]*$`)

func checkNavCoverage(root string, r *report) {
	mk := mustRead(root, "mkdocs.yml", r)
	i := strings.Index(mk, "\nnav:")
	if i < 0 {
		r.failf("mkdocs.yml: no nav: block")
		return
	}
	navBlock := mk[i:]
	inNav := map[string]bool{}
	for _, m := range navValueRE.FindAllStringSubmatch(navBlock, -1) {
		v := m[1]
		if !strings.HasSuffix(v, ".md") || strings.HasPrefix(v, "http") {
			continue
		}
		inNav[v] = true
	}
	// Every nav entry must resolve to a real file.
	for p := range inNav {
		if _, err := os.Stat(filepath.Join(root, "docs", p)); err != nil {
			r.failf("mkdocs.yml: nav entry docs/%s does not exist", p)
		}
	}
	// Every docs/**/*.md (except the GitHub-only pointer) must be in the nav.
	_ = filepath.WalkDir(filepath.Join(root, "docs"), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		rel, _ := filepath.Rel(filepath.Join(root, "docs"), path)
		if rel == "README.md" { // excluded in mkdocs.yml, pointer for GitHub browsing
			return nil
		}
		if !inNav[rel] {
			r.failf("mkdocs.yml: docs/%s is not in the nav", rel)
		}
		return nil
	})
}

// --- helpers -------------------------------------------------------------------

// findTableRow returns the first Markdown table row whose first cell is `name`.
func findTableRow(doc, name string) string {
	needle := "| `" + name + "`"
	for _, line := range strings.Split(doc, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), needle) {
			return line
		}
	}
	return ""
}
