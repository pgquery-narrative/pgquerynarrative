// Package embedding provides text embedding for RAG and similar-query retrieval.
package embedding

import "context"

// Embedder produces vector embeddings from text. When not configured,
// callers should skip RAG and semantic similar-query features.
type Embedder interface {
	// Embed returns the embedding vector for the given text, or an error.
	Embed(ctx context.Context, text string) ([]float32, error)
}
