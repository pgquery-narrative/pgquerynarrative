#!/usr/bin/env python3
"""Download NYC TLC Yellow Taxi Parquet files and COPY into opendata.yellow_trips.

Source: https://www.nyc.gov/site/tlc/about/tlc-trip-record-data.page
CDN:    https://d37ci6vzurychx.cloudfront.net/trip-data/yellow_tripdata_YYYY-MM.parquet

Examples:
  python3 tools/db/load_nyc_taxi.py --db-url 'postgres://...'
  MONTHS=2024-01 python3 tools/db/load_nyc_taxi.py --db-url '...'
"""

from __future__ import annotations

import argparse
import io
import os
import sys
import urllib.request
from pathlib import Path

# Parquet column names → Postgres column names
COLUMN_MAP = [
    ("tpep_pickup_datetime", "tpep_pickup_datetime"),
    ("tpep_dropoff_datetime", "tpep_dropoff_datetime"),
    ("passenger_count", "passenger_count"),
    ("trip_distance", "trip_distance"),
    ("PULocationID", "pulocation_id"),
    ("DOLocationID", "dolocation_id"),
    ("fare_amount", "fare_amount"),
    ("tip_amount", "tip_amount"),
    ("total_amount", "total_amount"),
    ("payment_type", "payment_type"),
]

PARQUET_COLS = [src for src, _ in COLUMN_MAP]
PG_COLS = [dst for _, dst in COLUMN_MAP]

COPY_SQL = f"""
COPY opendata.yellow_trips (
    {", ".join(PG_COLS)}
) FROM STDIN WITH (FORMAT csv, HEADER false, NULL '')
"""

CDN_TMPL = "https://d37ci6vzurychx.cloudfront.net/trip-data/yellow_tripdata_{month}.parquet"
DEFAULT_MONTHS = ("2024-01", "2024-02", "2024-03")
BATCH_SIZE = 250_000


def parse_months(raw: str | None) -> list[str]:
    if not raw or not raw.strip():
        return list(DEFAULT_MONTHS)
    months = [m.strip() for m in raw.split(",") if m.strip()]
    for m in months:
        if len(m) != 7 or m[4] != "-":
            raise SystemExit(f"invalid month {m!r}; expected YYYY-MM")
    return months


def ensure_deps():
    try:
        import pyarrow  # noqa: F401
        import psycopg  # noqa: F401
    except ImportError as e:
        raise SystemExit(
            "missing dependency: install with\n"
            "  python3 -m venv tools/db/.venv-nyc && "
            "tools/db/.venv-nyc/bin/pip install -r tools/db/requirements-nyc.txt\n"
            f"({e})"
        )


def download(month: str, cache_dir: Path) -> Path:
    cache_dir.mkdir(parents=True, exist_ok=True)
    dest = cache_dir / f"yellow_tripdata_{month}.parquet"
    if dest.exists() and dest.stat().st_size > 0:
        print(f"  cache hit: {dest}")
        return dest
    url = CDN_TMPL.format(month=month)
    print(f"  downloading {url}")
    tmp = dest.with_suffix(".parquet.partial")
    urllib.request.urlretrieve(url, tmp)
    tmp.replace(dest)
    return dest


def load_parquet(conn, path: Path) -> int:
    import pyarrow as pa
    import pyarrow.csv as pacsv
    import pyarrow.parquet as pq

    pf = pq.ParquetFile(path)
    available = set(pf.schema_arrow.names)
    missing = [c for c in PARQUET_COLS if c not in available]
    if missing:
        raise SystemExit(f"{path.name}: missing columns {missing}")

    write_opts = pacsv.WriteOptions(include_header=False)
    total = 0
    with conn.cursor() as cur:
        with cur.copy(COPY_SQL) as copy:
            for batch in pf.iter_batches(batch_size=BATCH_SIZE, columns=PARQUET_COLS):
                table = pa.Table.from_arrays(
                    [batch.column(src) for src, _ in COLUMN_MAP],
                    names=PG_COLS,
                )
                buf = io.BytesIO()
                pacsv.write_csv(table, buf, write_options=write_opts)
                copy.write(buf.getvalue())
                total += batch.num_rows
                if total % (BATCH_SIZE * 2) == 0 or total == batch.num_rows:
                    print(f"    … {total:,} rows")
    return total


def main() -> int:
    ensure_deps()
    import psycopg

    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--db-url",
        default=os.environ.get(
            "DB_URL",
            os.environ.get(
                "DATABASE_URL",
                "postgres://postgres:postgres@localhost:5432/pgquerynarrative?sslmode=disable",
            ),
        ),
        help="Postgres connection URL (superuser or role with INSERT on opendata)",
    )
    parser.add_argument(
        "--months",
        default=os.environ.get("MONTHS", ",".join(DEFAULT_MONTHS)),
        help="Comma-separated YYYY-MM list (default: 2024-01,2024-02,2024-03)",
    )
    parser.add_argument(
        "--cache-dir",
        default=os.environ.get(
            "NYC_TAXI_CACHE",
            str(Path(__file__).resolve().parent / ".cache" / "nyc-taxi"),
        ),
    )
    parser.add_argument(
        "--no-truncate",
        action="store_true",
        help="Append instead of truncating opendata.yellow_trips first",
    )
    args = parser.parse_args()
    months = parse_months(args.months)
    cache_dir = Path(args.cache_dir)

    print(f"months: {', '.join(months)}")
    print(f"cache:  {cache_dir}")
    print(f"db:     {args.db_url.split('@')[-1] if '@' in args.db_url else args.db_url}")

    paths = [download(month, cache_dir) for month in months]

    with psycopg.connect(args.db_url) as conn:
        conn.execute("SET statement_timeout = 0")
        with conn.cursor() as cur:
            cur.execute("SELECT to_regclass('opendata.yellow_trips') IS NOT NULL")
            if not cur.fetchone()[0]:
                raise SystemExit(
                    "opendata.yellow_trips missing — run migrations first "
                    "(make migrate / make migrate-docker)"
                )
            if not args.no_truncate:
                print("truncating opendata.yellow_trips …")
                cur.execute("TRUNCATE opendata.yellow_trips")
        conn.commit()

        grand = 0
        for month, path in zip(months, paths):
            print(f"loading {month} from {path.name} …")
            n = load_parquet(conn, path)
            conn.commit()
            print(f"  loaded {n:,} rows from {month}")
            grand += n

        with conn.cursor() as cur:
            cur.execute("ANALYZE opendata.yellow_trips")
            cur.execute("SELECT COUNT(*) FROM opendata.yellow_trips")
            count = cur.fetchone()[0]
            cur.execute(
                """
                SELECT pg_size_pretty(COALESCE(SUM(pg_total_relation_size(c.oid)), 0))
                FROM pg_class c
                JOIN pg_namespace n ON n.oid = c.relnamespace
                WHERE n.nspname = 'opendata'
                  AND c.relkind IN ('r', 'p')
                  AND c.relname LIKE 'yellow_trips%'
                """
            )
            size = cur.fetchone()[0]
        conn.commit()

    print(f"done: {grand:,} rows inserted this run; table count={count:,}; size≈{size}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
