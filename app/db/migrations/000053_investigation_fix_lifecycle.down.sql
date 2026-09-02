ALTER TABLE app.investigations
    DROP COLUMN IF EXISTS fix_measured_at,
    DROP COLUMN IF EXISTS fix_confirmed_mean_ms,
    DROP COLUMN IF EXISTS fix_baseline_mean_ms,
    DROP COLUMN IF EXISTS fix_applied_at,
    DROP COLUMN IF EXISTS fix_reference,
    DROP COLUMN IF EXISTS fix_status;
