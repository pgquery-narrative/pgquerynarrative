-- Showcase queries against real NYC TLC Yellow Taxi data (opendata.yellow_trips).
-- Run after: make migrate-docker && make seed-nyc-docker
-- Prefer schema-qualified names so queries work regardless of search_path order.

-- 1) Revenue by pickup zone (filter + group) — good EXPLAIN candidate
SELECT pulocation_id,
       COUNT(*) AS trips,
       ROUND(SUM(total_amount)::numeric, 2) AS revenue
FROM opendata.yellow_trips
WHERE tpep_pickup_datetime >= TIMESTAMP '2024-01-01'
  AND tpep_pickup_datetime <  TIMESTAMP '2024-02-01'
  AND pulocation_id IS NOT NULL
GROUP BY pulocation_id
ORDER BY revenue DESC
LIMIT 20;

-- 2) Monthly revenue (period / metrics / narrative path)
SELECT date_trunc('month', tpep_pickup_datetime) AS month,
       COUNT(*) AS trips,
       ROUND(SUM(total_amount)::numeric, 2) AS revenue,
       ROUND(AVG(trip_distance)::numeric, 2) AS avg_miles
FROM opendata.yellow_trips
WHERE tpep_pickup_datetime >= TIMESTAMP '2024-01-01'
  AND tpep_pickup_datetime <  TIMESTAMP '2024-04-01'
GROUP BY 1
ORDER BY 1;

-- 3) Tip rate by payment type
SELECT payment_type,
       COUNT(*) AS trips,
       ROUND(AVG(tip_amount)::numeric, 2) AS avg_tip,
       ROUND(
         (SUM(tip_amount) / NULLIF(SUM(fare_amount), 0) * 100)::numeric,
         1
       ) AS tip_pct_of_fare
FROM opendata.yellow_trips
WHERE fare_amount > 0
  AND payment_type IS NOT NULL
GROUP BY payment_type
ORDER BY trips DESC;

-- 4) Busy hours
SELECT EXTRACT(HOUR FROM tpep_pickup_datetime)::int AS hour_of_day,
       COUNT(*) AS trips,
       ROUND(AVG(total_amount)::numeric, 2) AS avg_total
FROM opendata.yellow_trips
WHERE tpep_pickup_datetime >= TIMESTAMP '2024-03-01'
  AND tpep_pickup_datetime <  TIMESTAMP '2024-04-01'
GROUP BY 1
ORDER BY 1;

-- 5) Distance vs fare outliers (long trips with low fare)
SELECT tpep_pickup_datetime,
       pulocation_id,
       dolocation_id,
       trip_distance,
       fare_amount,
       total_amount
FROM opendata.yellow_trips
WHERE trip_distance > 20
  AND fare_amount > 0
  AND fare_amount < 20
  AND tpep_pickup_datetime >= TIMESTAMP '2024-01-01'
  AND tpep_pickup_datetime <  TIMESTAMP '2024-04-01'
ORDER BY trip_distance DESC
LIMIT 50;
