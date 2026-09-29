-- Rename cron_expr to interval_expr (API only supports @every durations) and add timezone.
ALTER TABLE app.schedules RENAME COLUMN cron_expr TO interval_expr;
ALTER TABLE app.schedules ADD COLUMN IF NOT EXISTS timezone TEXT NOT NULL DEFAULT 'UTC';
ALTER TABLE app.schedules ALTER COLUMN destination_target DROP NOT NULL;
ALTER TABLE app.schedules ALTER COLUMN destination_target SET DEFAULT '';

COMMENT ON COLUMN app.schedules.interval_expr IS 'Interval expression using @every <duration> (e.g. @every 6h). Not cron.';
COMMENT ON COLUMN app.schedules.timezone IS 'IANA timezone for schedule evaluation (misfire: skip missed intervals; next_run advances from now).';
