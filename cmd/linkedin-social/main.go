// Command linkedin-social manages LinkedIn post drafts for PgQueryNarrative with an
// approval gate: nothing is posted until a draft is moved to approved/, then you
// run post (manually or from cron). LinkedIn requires a developer app, OAuth token
// with w_member_social, and env vars—see "linkedin-social help".
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	linkedinPostsURL    = "https://api.linkedin.com/rest/posts"
	linkedinUserInfoURL = "https://api.linkedin.com/v2/userinfo"
	defaultAPIVersion   = "202504"
	maxCommentaryWarn   = 2800
	httpTimeout         = 60 * time.Second
)

var httpClient = &http.Client{Timeout: httpTimeout}

// Draft is a queued LinkedIn post. Only drafts in approved/ are eligible for post.
type Draft struct {
	ID        string `json:"id"`
	CreatedAt string `json:"created_at"`
	Title     string `json:"title,omitempty"`
	Body      string `json:"body"`
	Template  string `json:"template,omitempty"`
}

type postRequest struct {
	Author                    string       `json:"author"`
	Commentary                string       `json:"commentary"`
	Visibility                string       `json:"visibility"`
	Distribution              distribution `json:"distribution"`
	LifecycleState            string       `json:"lifecycleState"`
	IsReshareDisabledByAuthor bool         `json:"isReshareDisabledByAuthor"`
}

type distribution struct {
	FeedDistribution               string `json:"feedDistribution"`
	TargetEntities                 []any  `json:"targetEntities"`
	ThirdPartyDistributionChannels []any  `json:"thirdPartyDistributionChannels"`
}

type userInfo struct {
	Sub string `json:"sub"`
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	root, err := findRepoRoot()
	if err != nil {
		fatalf("%v", err)
	}
	base := filepath.Join(root, "automation", "linkedin")

	switch os.Args[1] {
	case "help", "-h", "--help":
		usage()
	case "draft":
		runDraft(base, root, os.Args[2:])
	case "list":
		runList(base)
	case "approve":
		runApprove(base, os.Args[2:])
	case "reject":
		runReject(base, os.Args[2:])
	case "show":
		runShow(base, os.Args[2:])
	case "post":
		runPost(base, os.Args[2:])
	case "whoami":
		runWhoami()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Print(`linkedin-social — LinkedIn drafts with manual approval for PgQueryNarrative

Workflow
  1. linkedin-social draft          # writes automation/linkedin/pending/draft-*.json
  2. linkedin-social show <file>     # review body
  3. linkedin-social approve <file>  # moves pending → approved (your approval step)
  4. linkedin-social post <file>     # publishes via API (or post --pick-first)

Commands
  draft [--template NAME] [--body-file PATH] [--highlight TEXT] [--git-recent]
  list
  show <pending|approved>/<name>.json
  approve <name>.json | pending/<name>.json
  reject <pending/<name>.json>   (deletes pending draft)
  post <approved/<name>.json> | post --pick-first [--dry-run]
  whoami                         (prints suggested LINKEDIN_AUTHOR_URN from token)
  help

LinkedIn setup (one-time)
  - Create an app at https://www.linkedin.com/developers/
  - Request "Sign In with LinkedIn using OpenID Connect" + w_member_social where available
  - OAuth: obtain a member access token with w_member_social
  - export LINKEDIN_ACCESS_TOKEN=...
  - linkedin-social whoami
  - export LINKEDIN_AUTHOR_URN="urn:li:person:..."   # from whoami

Optional env
  LINKEDIN_API_VERSION   (default ` + defaultAPIVersion + `)  → Linkedin-Version header

Nothing posts from pending/. Only files in approved/ are sent to LinkedIn.

`)
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func findRepoRoot() (string, error) {
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
			return "", errors.New("go.mod not found: run from repo root or a subdirectory")
		}
		dir = parent
	}
}

func runDraft(base, repoRoot string, args []string) {
	fs := flag.NewFlagSet("draft", flag.ExitOnError)
	tplName := fs.String("template", "default", "template name (automation/linkedin/templates/<name>.txt)")
	bodyFile := fs.String("body-file", "", "if set, use this file as the full post body instead of template")
	highlight := fs.String("highlight", "shipping small improvements and tightening the read-only SQL path.", "text for {{HIGHLIGHT}} in template")
	gitRecent := fs.Bool("git-recent", false, "append last 5 git commits (oneline) under the body")
	_ = fs.Parse(args)

	var body string
	if *bodyFile != "" {
		b, err := os.ReadFile(*bodyFile)
		if err != nil {
			fatalf("read body file: %v", err)
		}
		body = strings.TrimSpace(string(b))
	} else {
		p := filepath.Join(base, "templates", *tplName+".txt")
		// #nosec G304 -- tplName is a CLI flag set by the operator running this local admin
		// tool, not untrusted network input.
		raw, err := os.ReadFile(p)
		if err != nil {
			fatalf("read template %s: %v", p, err)
		}
		body = strings.TrimSpace(string(raw))
		body = strings.ReplaceAll(body, "{{DATE}}", time.Now().UTC().Format("2006-01-02"))
		body = strings.ReplaceAll(body, "{{HIGHLIGHT}}", *highlight)
	}
	if *gitRecent {
		// #nosec G204 -- repoRoot is derived internally (not user/network input); this is a
		// local admin CLI invoking a fixed git subcommand with fixed flags.
		out, err := exec.Command("git", "-C", repoRoot, "log", "-5", "--oneline", "--no-decorate").CombinedOutput()
		if err != nil {
			fatalf("git log: %v\n%s", err, out)
		}
		if strings.TrimSpace(string(out)) != "" {
			body += "\n\nRecent commits:\n" + strings.TrimSpace(string(out))
		}
	}
	if len(body) > maxCommentaryWarn {
		fmt.Fprintf(os.Stderr, "warning: body length %d (LinkedIn may cap commentary; trim if post fails)\n", len(body))
	}

	id := fmt.Sprintf("draft-%s", time.Now().UTC().Format("20060102-150405"))
	d := Draft{
		ID:        id,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Title:     id,
		Body:      body,
		Template:  *tplName,
	}
	outPath := filepath.Join(base, "pending", id+".json")
	if err := writeDraft(outPath, &d); err != nil {
		fatalf("%v", err)
	}
	fmt.Printf("wrote %s\n", outPath)
}

func writeDraft(path string, d *Draft) error {
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

func runList(base string) {
	for _, label := range []string{"pending", "approved"} {
		dir := filepath.Join(base, label)
		entries, err := os.ReadDir(dir)
		if err != nil {
			fatalf("read %s: %v", dir, err)
		}
		var names []string
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			names = append(names, e.Name())
		}
		sort.Strings(names)
		fmt.Printf("[%s]\n", label)
		if len(names) == 0 {
			fmt.Println("  (empty)")
			continue
		}
		for _, n := range names {
			fmt.Printf("  %s\n", n)
		}
		fmt.Println()
	}
}

func resolvePendingPath(base, arg string) string {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return ""
	}
	if strings.Contains(arg, "/") || strings.Contains(arg, string(filepath.Separator)) ||
		strings.HasPrefix(arg, "pending/") {
		p := arg
		if !filepath.IsAbs(p) {
			p = filepath.Join(base, filepath.FromSlash(arg))
		}
		return filepath.Clean(p)
	}
	return filepath.Join(base, "pending", filepath.Base(arg))
}

func fileMustBeInDir(file, dir string) bool {
	f, err := filepath.Abs(file)
	if err != nil {
		return false
	}
	d, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(d, f)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func runApprove(base string, args []string) {
	if len(args) < 1 {
		fatalf("usage: linkedin-social approve <pending/name.json>")
	}
	src := resolvePendingPath(base, args[0])
	pendingDir := filepath.Join(base, "pending")
	if !fileMustBeInDir(src, pendingDir) {
		fatalf("refuse to approve outside pending/: %s", src)
	}
	// #nosec G703 -- src is validated against pendingDir by fileMustBeInDir above; this is a
	// local admin CLI operating on args the invoking operator already controls, not network input.
	if _, err := os.Stat(src); err != nil {
		fatalf("stat: %v", err)
	}
	dst := filepath.Join(base, "approved", filepath.Base(src))
	// #nosec G703 -- src validated above; dst is base/approved/<basename>, confined to base.
	if err := os.Rename(src, dst); err != nil {
		fatalf("rename: %v", err)
	}
	fmt.Printf("approved → %s\n", dst)
}

func runReject(base string, args []string) {
	if len(args) < 1 {
		fatalf("usage: linkedin-social reject <pending/name.json>")
	}
	src := resolvePendingPath(base, args[0])
	pendingDir := filepath.Join(base, "pending")
	if !fileMustBeInDir(src, pendingDir) {
		fatalf("refuse to reject outside pending/: %s", src)
	}
	// #nosec G703 -- src is validated against pendingDir by fileMustBeInDir above.
	if err := os.Remove(src); err != nil {
		fatalf("remove: %v", err)
	}
	fmt.Printf("deleted %s\n", src)
}

func runShow(base string, args []string) {
	if len(args) < 1 {
		fatalf("usage: linkedin-social show pending/draft-....json")
	}
	p := args[0]
	if !filepath.IsAbs(p) {
		p = filepath.Join(base, p)
	}
	p = filepath.Clean(p)
	if !fileMustBeInDir(p, base) {
		fatalf("refuse to show outside %s: %s", base, p)
	}
	// #nosec G703 -- p is validated against base above (fileMustBeInDir); this is a local
	// admin CLI operating on args the invoking operator already controls, not network input.
	b, err := os.ReadFile(p)
	if err != nil {
		fatalf("read: %v", err)
	}
	var d Draft
	if err := json.Unmarshal(b, &d); err != nil {
		fatalf("json: %v", err)
	}
	fmt.Println(d.Body)
}

func runPost(base string, args []string) {
	fs := flag.NewFlagSet("post", flag.ExitOnError)
	pickFirst := fs.Bool("pick-first", false, "post the lexicographically first JSON in approved/")
	dry := fs.Bool("dry-run", false, "print request JSON only; do not call LinkedIn")
	_ = fs.Parse(args)

	token := os.Getenv("LINKEDIN_ACCESS_TOKEN")
	author := os.Getenv("LINKEDIN_AUTHOR_URN")
	apiVer := os.Getenv("LINKEDIN_API_VERSION")
	if apiVer == "" {
		apiVer = defaultAPIVersion
	}

	var path string
	if *pickFirst {
		dir := filepath.Join(base, "approved")
		entries, err := os.ReadDir(dir)
		if err != nil {
			fatalf("read approved: %v", err)
		}
		var names []string
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		if len(names) == 0 {
			fatalf("no approved drafts")
		}
		path = filepath.Join(dir, names[0])
	} else {
		if fs.NArg() < 1 {
			fatalf("usage: linkedin-social post approved/name.json  OR  post --pick-first")
		}
		arg := fs.Arg(0)
		switch {
		case filepath.IsAbs(arg):
			path = filepath.Clean(arg)
		case strings.Contains(arg, "/") || strings.Contains(arg, string(filepath.Separator)) ||
			strings.HasPrefix(arg, "approved/"):
			path = filepath.Clean(filepath.Join(base, filepath.FromSlash(arg)))
		default:
			path = filepath.Join(base, "approved", filepath.Base(arg))
		}
	}
	approvedDir := filepath.Join(base, "approved")
	if !fileMustBeInDir(path, approvedDir) {
		fatalf("refuse to post outside approved/: %s", path)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		fatalf("read: %v", err)
	}
	var d Draft
	if err := json.Unmarshal(raw, &d); err != nil {
		fatalf("json: %v", err)
	}
	if strings.TrimSpace(d.Body) == "" {
		fatalf("draft body is empty")
	}

	if *dry {
		fmt.Println(string(mustJSON(postRequest{
			Author:     author,
			Commentary: d.Body,
			Visibility: "PUBLIC",
			Distribution: distribution{
				FeedDistribution:               "MAIN_FEED",
				TargetEntities:                 []any{},
				ThirdPartyDistributionChannels: []any{},
			},
			LifecycleState:            "PUBLISHED",
			IsReshareDisabledByAuthor: false,
		})))
		return
	}
	if token == "" {
		fatalf("LINKEDIN_ACCESS_TOKEN is not set")
	}
	if author == "" {
		fatalf("LINKEDIN_AUTHOR_URN is not set (run: linkedin-social whoami)")
	}

	payload := postRequest{
		Author:     author,
		Commentary: d.Body,
		Visibility: "PUBLIC",
		Distribution: distribution{
			FeedDistribution:               "MAIN_FEED",
			TargetEntities:                 []any{},
			ThirdPartyDistributionChannels: []any{},
		},
		LifecycleState:            "PUBLISHED",
		IsReshareDisabledByAuthor: false,
	}
	body := mustJSON(payload)

	req, err := http.NewRequest(http.MethodPost, linkedinPostsURL, bytes.NewReader(body))
	if err != nil {
		fatalf("request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Restli-Protocol-Version", "2.0.0")
	req.Header.Set("Linkedin-Version", apiVer)

	resp, err := httpClient.Do(req)
	if err != nil {
		fatalf("http: %v", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	postID := resp.Header.Get("x-restli-id")
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		fatalf("linkedin: %s\n%s", resp.Status, string(respBody))
	}
	fmt.Printf("posted ok status=%s x-restli-id=%q\n", resp.Status, postID)

	postedPath := filepath.Join(base, "posted", filepath.Base(path))
	if err := os.Rename(path, postedPath); err != nil {
		fatalf("archive to posted: %v", err)
	}
	fmt.Printf("archived → %s\n", postedPath)
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func runWhoami() {
	token := os.Getenv("LINKEDIN_ACCESS_TOKEN")
	if token == "" {
		fatalf("LINKEDIN_ACCESS_TOKEN is not set")
	}
	req, err := http.NewRequest(http.MethodGet, linkedinUserInfoURL, nil)
	if err != nil {
		fatalf("request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := httpClient.Do(req)
	if err != nil {
		fatalf("http: %v", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		fatalf("read body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		fatalf("userinfo: %s\n%s", resp.Status, string(b))
	}
	var u userInfo
	if err := json.Unmarshal(b, &u); err != nil {
		fatalf("json: %v", err)
	}
	if u.Sub == "" {
		fatalf("no sub in userinfo: %s", string(b))
	}
	urn := "urn:li:person:" + u.Sub
	fmt.Printf("LINKEDIN_AUTHOR_URN=%s\n", urn)
	fmt.Printf("export LINKEDIN_AUTHOR_URN=%q\n", urn)
}
