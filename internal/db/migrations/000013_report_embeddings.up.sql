CREATE TABLE IF NOT EXISTS app.report_embeddings (
    report_id UUID PRIMARY KEY REFERENCES app.reports(id) ON DELETE CASCADE,
    embedding jsonb NOT NULL,
    model text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_report_embeddings_updated_at ON app.report_embeddings(updated_at DESC);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'vector') THEN
    EXECUTE 'ALTER TABLE app.report_embeddings ADD COLUMN IF NOT EXISTS embedding_vector vector(768)';
    EXECUTE 'CREATE INDEX IF NOT EXISTS idx_report_embeddings_vector_cosine ON app.report_embeddings USING hnsw (embedding_vector vector_cosine_ops) WITH (m = 16, ef_construction = 64)';
  END IF;
END
$$;
