-- Enable pgvector when the library ships with the Postgres image (e.g. pgvector/pgvector:pg16).
-- Safe no-op on stock postgres:*-alpine (extension not in pg_available_extensions).
-- Re-applies column + HNSW indexes from 000007/000013 if extension is added after those migrations ran.

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_available_extensions WHERE name = 'vector') THEN
    EXECUTE 'CREATE EXTENSION IF NOT EXISTS vector';
    EXECUTE 'ALTER TABLE app.query_embeddings ADD COLUMN IF NOT EXISTS embedding_vector vector(768)';
    EXECUTE 'CREATE INDEX IF NOT EXISTS idx_query_embeddings_vector_cosine ON app.query_embeddings USING hnsw (embedding_vector vector_cosine_ops) WITH (m = 16, ef_construction = 64)';
    EXECUTE 'ALTER TABLE app.report_embeddings ADD COLUMN IF NOT EXISTS embedding_vector vector(768)';
    EXECUTE 'CREATE INDEX IF NOT EXISTS idx_report_embeddings_vector_cosine ON app.report_embeddings USING hnsw (embedding_vector vector_cosine_ops) WITH (m = 16, ef_construction = 64)';
  END IF;
END
$$;
