-- Extension may be shared; only drop vector column indexes when downgrading this migration.
DROP INDEX IF EXISTS app.idx_report_embeddings_vector_cosine;
DROP INDEX IF EXISTS app.idx_query_embeddings_vector_cosine;
ALTER TABLE app.report_embeddings DROP COLUMN IF EXISTS embedding_vector;
ALTER TABLE app.query_embeddings DROP COLUMN IF EXISTS embedding_vector;
