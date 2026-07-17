-- Real open data: NYC TLC Yellow Taxi trips (loaded via tools/db/load_nyc_taxi.py).
CREATE SCHEMA IF NOT EXISTS opendata;

GRANT USAGE ON SCHEMA opendata TO pgquerynarrative_readonly;
GRANT USAGE ON SCHEMA opendata TO pgquerynarrative_app;

CREATE TABLE IF NOT EXISTS opendata.yellow_trips (
    tpep_pickup_datetime  TIMESTAMPTZ NOT NULL,
    tpep_dropoff_datetime TIMESTAMPTZ,
    passenger_count       NUMERIC,
    trip_distance         NUMERIC,
    pulocation_id         INT,
    dolocation_id         INT,
    fare_amount           NUMERIC,
    tip_amount            NUMERIC,
    total_amount          NUMERIC,
    payment_type          INT
) PARTITION BY RANGE (tpep_pickup_datetime);

CREATE TABLE IF NOT EXISTS opendata.yellow_trips_2024_01
    PARTITION OF opendata.yellow_trips
    FOR VALUES FROM ('2024-01-01 00:00:00+00') TO ('2024-02-01 00:00:00+00');

CREATE TABLE IF NOT EXISTS opendata.yellow_trips_2024_02
    PARTITION OF opendata.yellow_trips
    FOR VALUES FROM ('2024-02-01 00:00:00+00') TO ('2024-03-01 00:00:00+00');

CREATE TABLE IF NOT EXISTS opendata.yellow_trips_2024_03
    PARTITION OF opendata.yellow_trips
    FOR VALUES FROM ('2024-03-01 00:00:00+00') TO ('2024-04-01 00:00:00+00');

-- Stray timestamps outside the default load window still land somewhere.
CREATE TABLE IF NOT EXISTS opendata.yellow_trips_default
    PARTITION OF opendata.yellow_trips DEFAULT;

CREATE INDEX IF NOT EXISTS idx_yellow_trips_2024_01_pulocation
    ON opendata.yellow_trips_2024_01 (pulocation_id);
CREATE INDEX IF NOT EXISTS idx_yellow_trips_2024_01_payment
    ON opendata.yellow_trips_2024_01 (payment_type);

CREATE INDEX IF NOT EXISTS idx_yellow_trips_2024_02_pulocation
    ON opendata.yellow_trips_2024_02 (pulocation_id);
CREATE INDEX IF NOT EXISTS idx_yellow_trips_2024_02_payment
    ON opendata.yellow_trips_2024_02 (payment_type);

CREATE INDEX IF NOT EXISTS idx_yellow_trips_2024_03_pulocation
    ON opendata.yellow_trips_2024_03 (pulocation_id);
CREATE INDEX IF NOT EXISTS idx_yellow_trips_2024_03_payment
    ON opendata.yellow_trips_2024_03 (payment_type);

GRANT SELECT ON ALL TABLES IN SCHEMA opendata TO pgquerynarrative_readonly;
ALTER DEFAULT PRIVILEGES IN SCHEMA opendata
    GRANT SELECT ON TABLES TO pgquerynarrative_readonly;

GRANT ALL ON ALL TABLES IN SCHEMA opendata TO pgquerynarrative_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA opendata
    GRANT ALL ON TABLES TO pgquerynarrative_app;

-- Include opendata on the readonly role search_path (AfterConnect also sets AllowedSchemas).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'pgquerynarrative_readonly') THEN
    ALTER ROLE pgquerynarrative_readonly SET search_path = demo, opendata;
  END IF;
END $$;
