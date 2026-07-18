// Package main runs an MCP server that exposes PgQueryNarrative as tools.
// Connect Claude (desktop or Cursor) to this server to run queries and generate reports via MCP.
//
// Prerequisites: PgQueryNarrative HTTP server must be running (e.g. make start-local).
// Set PGQUERYNARRATIVE_URL if the server is not at http://localhost:8080.
//
// Run: go run ./cmd/mcp-server
// Or build and run: go build -o bin/mcp-server ./cmd/mcp-server && ./bin/mcp-server
//
// Add to Claude desktop: MCP server command = path to this binary (stdio transport).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultBaseURL      = "http://localhost:8080"
	apiPrefix           = "/api/v1"
	httpClientTimeout   = 60 * time.Second
	defaultRunLimit     = 100
	defaultListLimit    = 20
	defaultSuggestLimit = 5
)

// Version is set at build time via -ldflags "-X main.Version=...". Default "dev".
var Version = "dev"

func main() {
	baseURL := os.Getenv("PGQUERYNARRATIVE_URL")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	apiKey := os.Getenv("PGQUERYNARRATIVE_API_KEY")
	client, err := newAPIClient(baseURL, apiKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid PGQUERYNARRATIVE_URL: %v\n", err)
		os.Exit(1)
	}

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "pgquerynarrative",
		Version: Version,
	}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "run_query",
		Description: "Run a read-only SQL query against the PgQueryNarrative database (demo schema). Returns columns and rows as JSON.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input RunQueryInput) (*mcp.CallToolResult, any, error) {
		limit := input.Limit
		if limit <= 0 {
			limit = defaultRunLimit
		}
		body, err := client.post(ctx, apiPrefix+"/queries/run", map[string]any{"sql": input.SQL, "limit": limit, "connection_id": input.ConnectionID})
		return toolResult(body, err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "generate_report",
		Description: "Run a SQL query and generate a narrative report (headline, takeaways, drivers, limitations, recommendations) using the configured LLM.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input GenerateReportInput) (*mcp.CallToolResult, any, error) {
		body, err := client.post(ctx, apiPrefix+"/reports/generate", map[string]any{"sql": input.SQL, "connection_id": input.ConnectionID})
		return toolResult(body, err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_saved_queries",
		Description: "List saved queries (optional limit and offset).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input ListSavedQueriesInput) (*mcp.CallToolResult, any, error) {
		limit := input.Limit
		if limit <= 0 {
			limit = defaultListLimit
		}
		path := listURL(apiPrefix+"/queries/saved", limit, input.Offset)
		if input.ConnectionID != "" {
			path += "&connection_id=" + url.QueryEscape(input.ConnectionID)
		}
		body, err := client.get(ctx, path)
		return toolResult(body, err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_report",
		Description: "Get a report by its ID (UUID).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input GetReportInput) (*mcp.CallToolResult, any, error) {
		body, err := client.get(ctx, apiPrefix+"/reports/"+url.PathEscape(input.ID))
		return toolResult(body, err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_reports",
		Description: "List generated reports (optional limit and offset).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input ListReportsInput) (*mcp.CallToolResult, any, error) {
		limit := input.Limit
		if limit <= 0 {
			limit = defaultListLimit
		}
		path := listURL(apiPrefix+"/reports", limit, input.Offset)
		if input.ConnectionID != "" {
			path += "&connection_id=" + url.QueryEscape(input.ConnectionID)
		}
		body, err := client.get(ctx, path)
		return toolResult(body, err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_schema",
		Description: "Returns the database schema available for querying (allowed schemas, tables, columns). Use this to see what tables and columns you can use in run_query.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input GetSchemaInput) (*mcp.CallToolResult, any, error) {
		path := apiPrefix + "/schema"
		if input.ConnectionID != "" {
			path = path + "?connection_id=" + url.QueryEscape(input.ConnectionID)
		}
		body, err := client.get(ctx, path)
		return toolResult(body, err)
	})
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_connections",
		Description: "List configured database connections (id and name).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input struct{}) (*mcp.CallToolResult, any, error) {
		body, err := client.get(ctx, apiPrefix+"/connections")
		return toolResult(body, err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_context",
		Description: "Returns combined context: schema (tables, columns) plus a list of saved queries (name, sql, description). Use this to understand the data model and existing saved queries before suggesting or running SQL.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input GetContextInput) (*mcp.CallToolResult, any, error) {
		savedLimit := input.SavedLimit
		if savedLimit <= 0 {
			savedLimit = defaultListLimit
		}
		schemaPath := apiPrefix + "/schema"
		if input.ConnectionID != "" {
			schemaPath = schemaPath + "?connection_id=" + url.QueryEscape(input.ConnectionID)
		}
		schemaBody, err1 := client.get(ctx, schemaPath)
		savedBody, err2 := client.get(ctx, listURL(apiPrefix+"/queries/saved", savedLimit, input.SavedOffset))
		out := buildContextResult(schemaBody, savedBody, err1, err2)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: out}}}, nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "suggest_queries",
		Description: "Suggests SQL queries based on optional intent (e.g. 'sales by category'). Returns curated examples and saved queries that match. Use the suggested SQL with run_query or refine before running.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input SuggestQueriesInput) (*mcp.CallToolResult, any, error) {
		limit := input.Limit
		if limit <= 0 {
			limit = defaultSuggestLimit
		}
		path := apiPrefix + "/suggestions/queries?"
		if input.Intent != "" {
			path += "intent=" + url.QueryEscape(input.Intent) + "&"
		}
		path += "limit=" + strconv.Itoa(limit)
		body, err := client.get(ctx, path)
		return toolResult(body, err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_schemas",
		Description: "List allowed database schemas, tables, and columns. Same as get_schema. Use this to see what you can query with run_query or ask_question.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input ListSchemasInput) (*mcp.CallToolResult, any, error) {
		path := apiPrefix + "/schema"
		if input.ConnectionID != "" {
			path += "?connection_id=" + url.QueryEscape(input.ConnectionID)
		}
		body, err := client.get(ctx, path)
		return toolResult(body, err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "ask_question",
		Description: "Ask a natural-language question; returns generated SQL and a narrative report (headline, takeaways). Uses the same NL→SQL flow as the web UI. Example: 'What were the top 5 products by revenue?'",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input AskQuestionInput) (*mcp.CallToolResult, any, error) {
		if input.Question == "" {
			return toolResult("", fmt.Errorf("question is required"))
		}
		body, err := client.post(ctx, apiPrefix+"/suggestions/ask", map[string]any{"question": input.Question, "connection_id": input.ConnectionID})
		return toolResult(body, err)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "explain_sql",
		Description: "Explain a SQL query in plain English (one or two sentences). Use after run_query or when the user asks what a query does. SQL must be read-only (SELECT or WITH).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, input ExplainSQLInput) (*mcp.CallToolResult, any, error) {
		if input.SQL == "" {
			return toolResult("", fmt.Errorf("sql is required"))
		}
		body, err := client.post(ctx, apiPrefix+"/suggestions/explain", map[string]any{"sql": input.SQL})
		return toolResult(body, err)
	})

	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fmt.Fprintf(os.Stderr, "mcp-server: %v\n", err)
		os.Exit(1)
	}
}

type RunQueryInput struct {
	SQL          string `json:"sql" jsonschema:"Read-only SQL query e.g. SELECT from demo.sales"`
	Limit        int    `json:"limit" jsonschema:"Max rows to return"`
	ConnectionID string `json:"connection_id,omitempty" jsonschema:"Optional configured connection ID (default connection when omitted)"`
}

type GenerateReportInput struct {
	SQL          string `json:"sql" jsonschema:"SQL query for the report e.g. SELECT category SUM amount FROM demo.sales GROUP BY category"`
	ConnectionID string `json:"connection_id,omitempty" jsonschema:"Optional configured connection ID"`
}

type ListSavedQueriesInput struct {
	Limit        int    `json:"limit" jsonschema:"Max items to return"`
	Offset       int    `json:"offset" jsonschema:"Offset for pagination"`
	ConnectionID string `json:"connection_id,omitempty" jsonschema:"Optional configured connection ID"`
}

type GetReportInput struct {
	ID string `json:"id" jsonschema:"Report UUID"`
}

type ListReportsInput struct {
	Limit        int    `json:"limit" jsonschema:"Max items to return"`
	Offset       int    `json:"offset" jsonschema:"Offset for pagination"`
	ConnectionID string `json:"connection_id,omitempty" jsonschema:"Optional configured connection ID"`
}

type GetSchemaInput struct {
	ConnectionID string `json:"connection_id,omitempty" jsonschema:"Optional configured connection ID"`
}

type GetContextInput struct {
	SavedLimit   int    `json:"saved_limit" jsonschema:"Max saved queries to include (default 20)"`
	SavedOffset  int    `json:"saved_offset" jsonschema:"Offset for saved queries (default 0)"`
	ConnectionID string `json:"connection_id,omitempty" jsonschema:"Optional configured connection ID for schema"`
}

type SuggestQueriesInput struct {
	Intent string `json:"intent" jsonschema:"Optional natural-language intent to match saved queries (e.g. sales by region)"`
	Limit  int    `json:"limit" jsonschema:"Max suggestions to return (default 5)"`
}

type ListSchemasInput struct {
	ConnectionID string `json:"connection_id,omitempty" jsonschema:"Optional configured connection ID"`
}

type AskQuestionInput struct {
	Question     string `json:"question" jsonschema:"Natural-language question (e.g. What were the top 5 products by revenue?)"`
	ConnectionID string `json:"connection_id,omitempty" jsonschema:"Optional configured connection ID"`
}

type ExplainSQLInput struct {
	SQL string `json:"sql" jsonschema:"Read-only SQL to explain (SELECT or WITH)"`
}

type apiClient struct {
	base   *url.URL
	apiKey string
	http   *http.Client
}

func newAPIClient(baseURL, apiKey string) (*apiClient, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return nil, err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("scheme must be http or https")
	}
	if u.Host == "" {
		return nil, fmt.Errorf("host is required")
	}
	// Normalize so ResolveReference joins paths predictably.
	if u.Path == "" {
		u.Path = "/"
	}
	return &apiClient{
		base:   u,
		apiKey: apiKey,
		http:   &http.Client{Timeout: httpClientTimeout},
	}, nil
}

func (c *apiClient) setAuth(req *http.Request) {
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
}

// resolveAPIURL joins path (and optional query) onto the configured base URL and
// refuses any result that would change scheme or host (SSRF guard).
func (c *apiClient) resolveAPIURL(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("empty API path")
	}
	ref, err := url.Parse(path)
	if err != nil {
		return "", err
	}
	if ref.IsAbs() || ref.Host != "" || ref.Scheme != "" || strings.HasPrefix(path, "//") {
		return "", fmt.Errorf("absolute URLs are not allowed as API paths")
	}
	u := c.base.ResolveReference(ref)
	if u.Scheme != c.base.Scheme || u.Host != c.base.Host {
		return "", fmt.Errorf("refused to leave configured API host")
	}
	// Rebuild from the trusted base host so the returned string is scheme+host locked.
	safe := url.URL{
		Scheme:   c.base.Scheme,
		Host:     c.base.Host,
		Path:     u.Path,
		RawPath:  u.RawPath,
		RawQuery: u.RawQuery,
		Fragment: u.Fragment,
	}
	return safe.String(), nil
}

func (c *apiClient) post(ctx context.Context, path string, body map[string]any) (string, error) {
	enc, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	abs, err := c.resolveAPIURL(path)
	if err != nil {
		return "", err
	}
	// abs is host-locked via resolveAPIURL (configured PGQUERYNARRATIVE_URL only).
	req, err := http.NewRequestWithContext(ctx, "POST", abs, bytes.NewReader(enc)) // #nosec G704
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	c.setAuth(req)
	return c.do(req)
}

func (c *apiClient) get(ctx context.Context, path string) (string, error) {
	abs, err := c.resolveAPIURL(path)
	if err != nil {
		return "", err
	}
	// abs is host-locked via resolveAPIURL (configured PGQUERYNARRATIVE_URL only).
	req, err := http.NewRequestWithContext(ctx, "GET", abs, nil) // #nosec G704
	if err != nil {
		return "", err
	}
	c.setAuth(req)
	return c.do(req)
}

func (c *apiClient) do(req *http.Request) (string, error) {
	resp, err := c.http.Do(req) // #nosec G704 -- host locked to configured PGQUERYNARRATIVE_URL
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("API %s %s: %d %s", req.Method, req.URL.Path, resp.StatusCode, string(b))
	}
	return string(b), nil
}

// toolResult returns a successful MCP tool result with body, or an error result if err is non-nil.
func toolResult(body string, err error) (*mcp.CallToolResult, any, error) {
	if err != nil {
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
		}, nil, nil
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: body}}}, nil, nil
}

func listURL(base string, limit, offset int) string {
	return fmt.Sprintf("%s?limit=%d&offset=%d", base, limit, offset)
}

// buildContextResult merges schema and saved-queries API responses into one text block.
// On partial failure, includes what succeeded and notes the error.
func buildContextResult(schemaBody, savedBody string, schemaErr, savedErr error) string {
	var b bytes.Buffer
	b.WriteString("=== Schema (queryable tables and columns) ===\n")
	if schemaErr != nil {
		b.WriteString("(schema unavailable: " + schemaErr.Error() + ")\n")
	} else {
		b.WriteString(schemaBody)
	}
	b.WriteString("\n\n=== Saved queries ===\n")
	if savedErr != nil {
		b.WriteString("(saved queries unavailable: " + savedErr.Error() + ")\n")
	} else {
		b.WriteString(savedBody)
	}
	return b.String()
}
