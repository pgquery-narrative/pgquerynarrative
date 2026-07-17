package embedding

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/pgquerynarrative/pgquerynarrative/app/db"
)

// EmbeddingVectorDimension is the size used for pgvector (e.g. nomic-embed-text).
const EmbeddingVectorDimension = 768

// SimilarQuery holds a saved query and its similarity score (0–1, higher is more similar).
type SimilarQuery struct {
	SavedQueryID string
	Name         string
	SQL          string
	Description  string
	Score        float64
}

// SimilarReport holds a report and its similarity score (0-1, higher is more similar).
type SimilarReport struct {
	ReportID     string
	Headline     string
	SQL          string
	ConnectionID string
	CreatedAt    string
	Similarity   float64
}

type scoredQuery struct {
	sim   SimilarQuery
	score float64
}

// Store persists and retrieves query embeddings for similar-query search and RAG.
type Store struct {
	appPool db.DB
}

// NewStore creates a store that uses the org-scoped app DB (writes to app.query_embeddings).
func NewStore(appPool db.DB) *Store {
	return &Store{appPool: appPool}
}

// Upsert saves or replaces the embedding for a saved query. Stores JSONB and, when
// pgvector is available, a native vector column for in-database semantic search.
func (s *Store) Upsert(ctx context.Context, savedQueryID string, embedding []float32, model string) error {
	raw, err := json.Marshal(embedding)
	if err != nil {
		return fmt.Errorf("marshal embedding: %w", err)
	}
	vectorLiteral := formatVectorForPG(embedding)
	_, err = s.appPool.Exec(ctx, `
		INSERT INTO app.query_embeddings (saved_query_id, embedding, model, updated_at, embedding_vector)
		VALUES ($1::uuid, $2::jsonb, $3, NOW(), $4::vector(768))
		ON CONFLICT (saved_query_id) DO UPDATE SET
			embedding = EXCLUDED.embedding,
			model = EXCLUDED.model,
			updated_at = NOW(),
			embedding_vector = EXCLUDED.embedding_vector
	`, savedQueryID, raw, model, vectorLiteral)
	if err != nil {
		// Fallback when pgvector column or extension is missing
		_, fallbackErr := s.appPool.Exec(ctx, `
			INSERT INTO app.query_embeddings (saved_query_id, embedding, model, updated_at)
			VALUES ($1::uuid, $2::jsonb, $3, NOW())
			ON CONFLICT (saved_query_id) DO UPDATE SET embedding = EXCLUDED.embedding, model = EXCLUDED.model, updated_at = NOW()
		`, savedQueryID, raw, model)
		if fallbackErr != nil {
			return fmt.Errorf("upsert embedding: %w", fallbackErr)
		}
	}
	return nil
}

// formatVectorForPG returns a PostgreSQL vector literal "[a,b,c,...]" for pgvector.
func formatVectorForPG(v []float32) string {
	if len(v) == 0 {
		return "[]"
	}
	b := make([]string, len(v))
	for i := range v {
		b[i] = fmt.Sprintf("%g", v[i])
	}
	return "[" + strings.Join(b, ",") + "]"
}

// FindSimilar returns saved queries most similar to the given embedding (cosine similarity).
// Uses pgvector for in-database search when available; otherwise falls back to in-memory ranking.
func (s *Store) FindSimilar(ctx context.Context, queryEmbedding []float32, limit int) ([]SimilarQuery, error) {
	if limit <= 0 {
		limit = 5
	}
	// Try pgvector path first (in-database semantic search)
	vectorLiteral := formatVectorForPG(queryEmbedding)
	rows, err := s.appPool.Query(ctx, `
		SELECT qe.saved_query_id::text, sq.name, sq.sql, COALESCE(sq.description, ''),
		       (1 - (qe.embedding_vector <=> $1::vector(768))) AS score
		FROM app.query_embeddings qe
		JOIN app.saved_queries sq ON sq.id = qe.saved_query_id
		WHERE qe.embedding_vector IS NOT NULL
		ORDER BY qe.embedding_vector <=> $1::vector(768)
		LIMIT $2
	`, vectorLiteral, limit)
	if err == nil {
		defer rows.Close()
		return scanSimilarRows(rows)
	}
	// Fallback: load all and rank in memory
	return s.findSimilarInMemory(ctx, queryEmbedding, limit)
}

func scanSimilarRows(rows pgx.Rows) ([]SimilarQuery, error) {
	var out []SimilarQuery
	for rows.Next() {
		var sim SimilarQuery
		if err := rows.Scan(&sim.SavedQueryID, &sim.Name, &sim.SQL, &sim.Description, &sim.Score); err != nil {
			return nil, err
		}
		out = append(out, sim)
	}
	return out, rows.Err()
}

func (s *Store) findSimilarInMemory(ctx context.Context, queryEmbedding []float32, limit int) ([]SimilarQuery, error) {
	rows, err := s.appPool.Query(ctx, `
		SELECT qe.saved_query_id::text, qe.embedding, sq.name, sq.sql, COALESCE(sq.description, '')
		FROM app.query_embeddings qe
		JOIN app.saved_queries sq ON sq.id = qe.saved_query_id
	`)
	if err != nil {
		return nil, fmt.Errorf("query embeddings: %w", err)
	}
	defer rows.Close()

	type row struct {
		id            string
		embeddingJSON []byte
		name          string
		sql           string
		description   string
	}
	var candidates []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.embeddingJSON, &r.name, &r.sql, &r.description); err != nil {
			return nil, err
		}
		candidates = append(candidates, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	queryNorm := norm(queryEmbedding)
	if queryNorm == 0 {
		return nil, nil
	}
	var scoredList []scoredQuery
	for _, c := range candidates {
		var vec []float32
		if err := json.Unmarshal(c.embeddingJSON, &vec); err != nil {
			continue
		}
		score := cosineSimilarity(queryEmbedding, vec)
		scoredList = append(scoredList, scoredQuery{
			sim: SimilarQuery{
				SavedQueryID: c.id,
				Name:         c.name,
				SQL:          c.sql,
				Description:  c.description,
				Score:        score,
			},
			score: score,
		})
	}
	sortByScoreDesc(scoredList)
	out := make([]SimilarQuery, 0, limit)
	for i := 0; i < len(scoredList) && i < limit; i++ {
		out = append(out, scoredList[i].sim)
	}
	return out, nil
}

func norm(v []float32) float64 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum <= 0 {
		return 0
	}
	return sqrt(sum)
}

func sqrt(x float64) float64 {
	if x <= 0 {
		return 0
	}
	// Newton step
	y := x
	for i := 0; i < 10; i++ {
		next := (y + x/y) / 2
		if next == y {
			return y
		}
		y = next
	}
	return y
}

func cosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
	}
	na := norm(a)
	nb := norm(b)
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (na * nb)
}

func sortByScoreDesc(list []scoredQuery) {
	// Simple insertion sort for small N
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && list[j].score > list[j-1].score; j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
}

// UpsertReport saves or replaces the embedding for a generated report.
func (s *Store) UpsertReport(ctx context.Context, reportID string, embedding []float32, model string) error {
	raw, err := json.Marshal(embedding)
	if err != nil {
		return fmt.Errorf("marshal report embedding: %w", err)
	}
	vectorLiteral := formatVectorForPG(embedding)
	_, err = s.appPool.Exec(ctx, `
		INSERT INTO app.report_embeddings (report_id, embedding, model, updated_at, embedding_vector)
		VALUES ($1::uuid, $2::jsonb, $3, NOW(), $4::vector(768))
		ON CONFLICT (report_id) DO UPDATE SET
			embedding = EXCLUDED.embedding,
			model = EXCLUDED.model,
			updated_at = NOW(),
			embedding_vector = EXCLUDED.embedding_vector
	`, reportID, raw, model, vectorLiteral)
	if err != nil {
		_, fallbackErr := s.appPool.Exec(ctx, `
			INSERT INTO app.report_embeddings (report_id, embedding, model, updated_at)
			VALUES ($1::uuid, $2::jsonb, $3, NOW())
			ON CONFLICT (report_id) DO UPDATE SET embedding = EXCLUDED.embedding, model = EXCLUDED.model, updated_at = NOW()
		`, reportID, raw, model)
		if fallbackErr != nil {
			return fmt.Errorf("upsert report embedding: %w", fallbackErr)
		}
	}
	return nil
}

// FindSimilarReports returns reports most similar to the given embedding.
func (s *Store) FindSimilarReports(ctx context.Context, queryEmbedding []float32, connectionID string, limit int) ([]SimilarReport, error) {
	if limit <= 0 {
		limit = 5
	}
	vectorLiteral := formatVectorForPG(queryEmbedding)
	rows, err := s.appPool.Query(ctx, `
		SELECT re.report_id::text, COALESCE(r.narrative_md, ''), r.sql, r.connection_id, r.created_at::text,
		       (1 - (re.embedding_vector <=> $1::vector(768))) AS score
		FROM app.report_embeddings re
		JOIN app.reports r ON r.id = re.report_id
		WHERE re.embedding_vector IS NOT NULL
		  AND ($2 = '' OR r.connection_id = $2)
		ORDER BY re.embedding_vector <=> $1::vector(768)
		LIMIT $3
	`, vectorLiteral, connectionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]SimilarReport, 0, limit)
	for rows.Next() {
		var r SimilarReport
		if scanErr := rows.Scan(&r.ReportID, &r.Headline, &r.SQL, &r.ConnectionID, &r.CreatedAt, &r.Similarity); scanErr != nil {
			return nil, scanErr
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
