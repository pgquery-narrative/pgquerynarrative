// Command openapi-strip-examples removes example payloads from the generated
// OpenAPI documents.
//
// goa seeds an `example` value for every schema it emits, and those seeded
// values dominate the artifacts: openapi3.json is 2.4 MB, of which ~97% is
// examples. Worse, adding a single field to a widely-referenced type re-expands
// the examples everywhere it appears — one field changed 55,000 lines of
// openapi3.yaml, which is why these files ended up behind `-diff` in
// .gitattributes and stopped being reviewable at all.
//
// The examples carry no contract information: request/response shapes come from
// the schemas, which is also all the TypeScript generator reads. Stripping them
// keeps the specs in the tree as publishable artifacts and inside the
// `git diff --exit-code` codegen gate, while making their diffs readable.
//
// YAML output preserves goa's key order (the document is walked as a yaml.Node).
// JSON output is re-marshalled through encoding/json, so its keys come out
// sorted — deterministic, and irrelevant to a JSON consumer. It is also
// indented rather than left on goa's single minified line, so a changed field
// shows up as a few lines in review instead of one 80 KB line.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// exampleKeys are the OpenAPI keywords holding seeded sample payloads.
var exampleKeys = map[string]bool{"example": true, "examples": true}

func main() {
	quiet := flag.Bool("quiet", false, "only report failures")
	flag.Parse()
	paths := flag.Args()
	if len(paths) == 0 {
		fmt.Fprintln(os.Stderr, "usage: openapi-strip-examples [-quiet] <file>...")
		os.Exit(2)
	}
	for _, p := range paths {
		before, after, err := stripFile(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "openapi-strip-examples: %s: %v\n", p, err)
			os.Exit(1)
		}
		if !*quiet {
			fmt.Printf("  %-34s %9d -> %9d bytes\n", filepath.Base(p), before, after)
		}
	}
}

func stripFile(path string) (before, after int, err error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, 0, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, err
	}
	before = len(raw)

	var out []byte
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		out, err = stripYAML(raw)
	case ".json":
		out, err = stripJSON(raw)
	default:
		return 0, 0, fmt.Errorf("unsupported extension %q", filepath.Ext(path))
	}
	if err != nil {
		return 0, 0, err
	}

	// Only rewrite on an actual change so repeated runs leave mtimes alone.
	if string(out) == string(raw) {
		return before, before, nil
	}
	// Rewrite in place with the mode goa already gave the file, rather than
	// imposing one: these are published artifacts, and the generator owns their
	// permissions.
	if err := os.WriteFile(path, out, info.Mode().Perm()); err != nil {
		return 0, 0, err
	}
	return before, len(out), nil
}

func stripYAML(raw []byte) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	stripNode(&doc, false)
	var b strings.Builder
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, fmt.Errorf("encode: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("close: %w", err)
	}
	return []byte(b.String()), nil
}

// stripNode removes example mappings in place. inProperties is true when the
// node is the value of a JSON Schema "properties" key, where a child named
// "example" is a real field of the API rather than an OpenAPI keyword.
func stripNode(n *yaml.Node, inProperties bool) {
	if n == nil {
		return
	}
	switch n.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, c := range n.Content {
			stripNode(c, false)
		}
	case yaml.MappingNode:
		kept := make([]*yaml.Node, 0, len(n.Content))
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if !inProperties && exampleKeys[k.Value] {
				continue
			}
			stripNode(v, k.Value == "properties")
			kept = append(kept, k, v)
		}
		n.Content = kept
	}
}

func stripJSON(raw []byte) ([]byte, error) {
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	out, err := json.MarshalIndent(stripAny(doc, false), "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode: %w", err)
	}
	return append(out, '\n'), nil
}

func stripAny(v any, inProperties bool) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if !inProperties && exampleKeys[k] {
				continue
			}
			out[k] = stripAny(val, k == "properties")
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = stripAny(val, false)
		}
		return out
	default:
		return v
	}
}
