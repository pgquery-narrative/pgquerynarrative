ALTER TABLE app.schedules DROP COLUMN IF EXISTS timezone;
ALTER TABLE app.schedules RENAME COLUMN interval_expr TO cron_expr;
ALTER TABLE app.schedules ALTER COLUMN destination_target SET DEFAULT '';
ALTER TABLE app.schedules ALTER COLUMN destination_target SET NOT NULL;
