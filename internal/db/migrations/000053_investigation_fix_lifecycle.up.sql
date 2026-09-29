-- Close the loop: track a shipped fix from proposed -> verified -> applied ->
-- confirmed/regressed, and record the numbers needed to re-measure it against
-- pg_stat_statements after deploy.

ALTER TABLE app.investigations
    ADD COLUMN IF NOT EXISTS fix_status TEXT NOT NULL DEFAULT 'proposed'
        CHECK (fix_status IN ('proposed', 'verified', 'applied', 'confirmed', 'regressed', 'abandoned')),
    ADD COLUMN IF NOT EXISTS fix_reference          TEXT,                -- PR / ticket URL
    ADD COLUMN IF NOT EXISTS fix_applied_at         TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS fix_baseline_mean_ms   DOUBLE PRECISION,    -- linked-query mean at apply time
    ADD COLUMN IF NOT EXISTS fix_confirmed_mean_ms  DOUBLE PRECISION,    -- linked-query mean at last re-measure
    ADD COLUMN IF NOT EXISTS fix_measured_at        TIMESTAMPTZ;

-- Existing complete investigations were proposals that were never tracked further.
UPDATE app.investigations SET fix_status = 'proposed' WHERE fix_status IS NULL;
