DROP INDEX IF EXISTS app.idx_report_embeddings_vector_cosine;
ALTER TABLE app.report_embeddings DROP COLUMN IF EXISTS embedding_vector;
DROP INDEX IF EXISTS app.idx_report_embeddings_updated_at;
DROP TABLE IF EXISTS app.report_embeddings;
