CREATE TABLE IF NOT EXISTS app.rate_limit_buckets (
    bucket_key   TEXT PRIMARY KEY,
    tokens       DOUBLE PRECISION NOT NULL,
    last_refill  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_rate_limit_buckets_last_refill ON app.rate_limit_buckets (last_refill);
