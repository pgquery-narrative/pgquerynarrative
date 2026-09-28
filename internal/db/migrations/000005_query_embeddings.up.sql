-- Store embeddings for saved queries to support similar-query retrieval and RAG.
-- Dimension depends on embedding model (e.g. 768 for nomic-embed-text). Stored as JSONB for portability.
CREATE TABLE IF NOT EXISTS app.query_embeddings (
    saved_query_id UUID PRIMARY KEY REFERENCES app.saved_queries(id) ON DELETE CASCADE,
    embedding jsonb NOT NULL,
    model text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_query_embeddings_updated_at ON app.query_embeddings(updated_at DESC);
