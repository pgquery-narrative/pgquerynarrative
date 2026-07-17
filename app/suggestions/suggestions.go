// Package suggestions provides query suggestion logic: curated example queries,
// matching saved queries by intent (keyword/substring), and semantic similar-query retrieval.
package suggestions

import (
	"context"
	"strings"

	suggestions "github.com/pgquerynarrative/pgquerynarrative/api/gen/suggestions"
	"github.com/pgquerynarrative/pgquerynarrative/app/db"
	"github.com/pgquerynarrative/pgquerynarrative/app/embedding"
)

const (
	defaultLimit          = 5
	maxLimit              = 20
	descriptionTruncateAt = 60
)

// Curated example queries for the demo schema (demo.sales).
// These are always included when limit allows, so the AI has starting points.
var curatedQueries = []*suggestions.QuerySuggestion{
	{
		SQL:    "SELECT product_category, SUM(total_amount) AS total FROM demo.sales GROUP BY product_category ORDER BY total DESC",
		Title:  "Sales by category (aggregate)",
		Source: "curated",
	},
	{
		SQL:    "SELECT date, SUM(total_amount) AS daily_total FROM demo.sales GROUP BY date ORDER BY date",
		Title:  "Daily sales over time",
		Source: "curated",
	},
	{
		SQL:    "SELECT region, product_category, SUM(quantity) AS qty FROM demo.sales GROUP BY region, product_category ORDER BY region, qty DESC",
		Title:  "Quantity by region and category",
		Source: "curated",
	},
}

// Suggester returns suggested SQL (curated + saved-query matches by intent,
// and optional embedding-based similar queries).
type Suggester struct {
	appPool  db.DB
	embedder embedding.Embedder
	store    *embedding.Store
}

// NewSuggester creates a suggester that uses the app pool to list saved queries
// for intent matching. Similar() will return no results when embeddings are not configured.
func NewSuggester(appPool db.DB) *Suggester {
	return &Suggester{appPool: appPool}
}

// NewSuggesterWithEmbedding creates a suggester with optional embedding-based similar-query
// retrieval. embedder and store may be nil to disable Similar().
func NewSuggesterWithEmbedding(appPool db.DB, embedder embedding.Embedder, store *embedding.Store) *Suggester {
	return &Suggester{appPool: appPool, embedder: embedder, store: store}
}

// Queries implements the suggestions service: returns curated examples plus
// saved queries matching the optional intent, up to limit.
func (s *Suggester) Queries(ctx context.Context, payload *suggestions.QueriesPayload) (*suggestions.SuggestedQueriesResult, error) {
	limit := clampLimit(int(payload.Limit), defaultLimit, maxLimit)

	var out []*suggestions.QuerySuggestion

	// Add curated (up to limit)
	for _, c := range curatedQueries {
		if len(out) >= limit {
			break
		}
		out = append(out, &suggestions.QuerySuggestion{
			SQL:    c.SQL,
			Title:  c.Title,
			Source: c.Source,
		})
	}

	// If intent provided, add saved queries that match (name, description, or sql)
	intent := ""
	if payload.Intent != nil {
		intent = strings.TrimSpace(*payload.Intent)
	}
	if intent != "" && len(out) < limit {
		saved, err := s.matchSavedQueries(ctx, intent, limit-len(out))
		if err == nil {
			for _, q := range saved {
				out = append(out, q)
				if len(out) >= limit {
					break
				}
			}
		}
	}

	return &suggestions.SuggestedQueriesResult{Suggestions: out}, nil
}

// Similar returns saved queries semantically similar to the given text using
// stored embeddings. When embeddings are not configured, returns empty suggestions.
func (s *Suggester) Similar(ctx context.Context, payload *suggestions.SimilarPayload) (*suggestions.SuggestedQueriesResult, error) {
	if s.embedder == nil || s.store == nil {
		return &suggestions.SuggestedQueriesResult{Suggestions: []*suggestions.QuerySuggestion{}}, nil
	}
	text := ""
	if payload.Text != nil {
		text = strings.TrimSpace(*payload.Text)
	}
	if text == "" {
		return &suggestions.SuggestedQueriesResult{Suggestions: []*suggestions.QuerySuggestion{}}, nil
	}
	vec, err := s.embedder.Embed(ctx, text)
	if err != nil {
		return &suggestions.SuggestedQueriesResult{Suggestions: []*suggestions.QuerySuggestion{}}, nil
	}
	limit := clampLimit(int(payload.Limit), defaultLimit, maxLimit)
	similar, err := s.store.FindSimilar(ctx, vec, limit)
	if err != nil {
		return &suggestions.SuggestedQueriesResult{Suggestions: []*suggestions.QuerySuggestion{}}, nil
	}
	out := make([]*suggestions.QuerySuggestion, len(similar))
	for i, q := range similar {
		title := q.Name
		if q.Description != "" {
			title = q.Name + ": " + truncate(q.Description, descriptionTruncateAt)
		}
		out[i] = &suggestions.QuerySuggestion{
			SQL:    q.SQL,
			Title:  title,
			Source: "similar",
		}
	}
	return &suggestions.SuggestedQueriesResult{Suggestions: out}, nil
}

// matchSavedQueries returns saved queries whose name, description, or sql
// contain the intent string (case-insensitive substring), up to max.
// Uses position() so intent is not interpreted as a LIKE pattern.
func (s *Suggester) matchSavedQueries(ctx context.Context, intent string, max int) ([]*suggestions.QuerySuggestion, error) {
	lowerIntent := strings.ToLower(intent)
	rows, err := s.appPool.Query(ctx, `
		SELECT name, sql, COALESCE(description, '')
		FROM app.saved_queries
		WHERE position($1 in lower(name)) > 0
		   OR position($1 in lower(COALESCE(description, ''))) > 0
		   OR position($1 in lower(sql)) > 0
		ORDER BY updated_at DESC
		LIMIT $2
	`, lowerIntent, max)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*suggestions.QuerySuggestion
	for rows.Next() {
		var name, sql, desc string
		if err := rows.Scan(&name, &sql, &desc); err != nil {
			return nil, err
		}
		title := name
		if desc != "" {
			title = name + ": " + truncate(desc, descriptionTruncateAt)
		}
		result = append(result, &suggestions.QuerySuggestion{
			SQL:    sql,
			Title:  title,
			Source: "saved",
		})
	}
	return result, rows.Err()
}

func clampLimit(value, defaultVal, max int) int {
	if value < 1 {
		return defaultVal
	}
	if value > max {
		return max
	}
	return value
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
