DROP INDEX IF EXISTS app.idx_query_embeddings_vector_cosine;
ALTER TABLE app.query_embeddings DROP COLUMN IF EXISTS embedding_vector;
-- To remove the extension, run as superuser: psql -d pgquerynarrative -c 'DROP EXTENSION IF EXISTS vector;'
