# Quick start: pqn in your database

`pqn` finds the statements that cost the most, proposes a rewrite from the plan, and checks that it returns the same
rows faster. It runs from a terminal against PostgreSQL, with no PgQueryNarrative server. This page takes about ten
minutes on a throwaway Docker database. For your own server, see [Install the pqn extension](pqn-installation.md).

`bash` blocks are run by `make verify-pqn-docs`. `text` blocks are real output; your timings will differ. You need
Docker, Go 1.26 with a C toolchain, and a clone of this repository.

## 1. Start PostgreSQL with pqn

The image creates the extension, its roles and its audit ledger on first start, and preloads `pg_stat_statements`:

```bash
make build-pqn-image
docker rm -f pqn-quickstart >/dev/null 2>&1 || true
docker run -d --name pqn-quickstart -p 127.0.0.1:5433:5432 \
  -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=app pqn-postgres:18
until docker exec pqn-quickstart pg_isready -U postgres -h 127.0.0.1 -d app >/dev/null 2>&1; do sleep 1; done
sleep 2
pg() { docker exec -i pqn-quickstart psql -X -q -At -U "$1" -d app "${@:2}"; }
pg postgres -c "SELECT extname || ' ' || extversion FROM pg_extension WHERE extname = 'pqn'"
```

```text
pqn 1.1
```

## 2. Load a slow-query lab and expose it

One table of 800,000 orders. The slow statement wraps the indexed `created_at` in a function, so the index cannot be used.

```bash
pg postgres <<'SQL'
CREATE SCHEMA shop;
CREATE TABLE shop.orders AS
  SELECT g AS id,
         (1 + (g::bigint * 7919) % 5000)::int AS customer_id,
         timestamptz '2024-01-01' + ((g::bigint * 13) % 730)::int * interval '1 day'
           + (g % 86400) * interval '1 second' AS created_at,
         (ARRAY['paid','new','refunded','shipped'])[1 + g % 4] AS status,
         ((g::bigint * 31) % 9973)::numeric / 10 AS amount,
         'private note ' || g AS notes
    FROM generate_series(1, 800000) g;
ALTER TABLE shop.orders ADD PRIMARY KEY (id);
CREATE INDEX orders_created_idx ON shop.orders (created_at);
ANALYZE shop.orders;
SQL
```

Analysts read views, not tables. Expose the columns they may see (`notes` is left out). `expose_sql` prints the SQL for review,
`expose` runs it. `full` scope lets `pqn` plan real statements that touch every column, while `run` still returns only the
listed ones ([scopes](pqn-installation.md#3-expose-the-tables-analysts-may-see)):

```bash
pg postgres -c "SELECT pqn_api.expose_sql('shop.orders', ARRAY['id','customer_id','created_at','status','amount'], 'orders', 'full')"
pg postgres -c "SELECT pqn_api.expose('shop.orders', ARRAY['id','customer_id','created_at','status','amount'], 'orders', 'full') IS NOT NULL"
```

```text
GRANT USAGE ON SCHEMA shop TO pqn_owner;
GRANT SELECT ON shop.orders TO pqn_owner;  -- scope full: plans and row counts cover every column
CREATE VIEW pqn.orders AS SELECT id, customer_id, created_at, status, amount FROM shop.orders;  -- as pqn_owner
GRANT SELECT ON pqn.orders TO pqn_reader;
INSERT INTO pqn.exposed (schema_name, table_name, view_name, scope, columns) VALUES ('shop', 'orders', 'orders', 'full', '{id,customer_id,created_at,status,amount}');  -- as pqn_owner
t
```

## 3. Add a person and build the tool

People log in as themselves. `enroll` sets their group and limits. The tool stores no secret.

```bash
pg postgres -c "CREATE ROLE alice LOGIN PASSWORD 'alice-pw'"
pg postgres -c "SELECT pqn_api.enroll('alice', 'analyst', '120s')" >/dev/null
make build-pqn
export PATH="$PWD/bin:$PATH"
export PQN_DSN='postgres://alice:alice-pw@127.0.0.1:5433/app'
pqn version
```

## 4. Find the slow statement and investigate it

Reset the statistics, run the "application's" slow statement three times, and rank what was recorded:

```bash
pg postgres -c "SELECT pg_stat_statements_reset()" >/dev/null
for d in 01 02 03; do
  pg postgres -c "SELECT id, customer_id, amount FROM shop.orders WHERE date_trunc('day', created_at) = '2025-03-$d'" >/dev/null
done
pqn top -n 3
```

```text
queryid               calls  mean     total     statement
--------------------  -----  -------  --------  --------------------------------------------------------------------------------
-7409685708004192759  3      36.6 ms  109.9 ms  SELECT id, customer_id, amount FROM shop.orders WHERE date_trunc($1, created_at…
-6576252161252119529  1      0.3 ms   0.3 ms    SELECT pg_stat_statements_reset()

Investigate one:  pqn investigate --queryid -7409685708004192759
```

`pg_stat_statements` stores statements without their values (`$1`, `$2`), so `pqn` needs a `--bind` per placeholder to *measure* a rewrite:

```bash
QID="$(pqn top -n 1 | awk 'NR == 3 { print $1 }')"
pqn investigate --queryid="$QID" --bind day --bind 2025-03-01 --title "daily orders are slow"
```

```text
PgQueryNarrative investigation #1
============================================================

Statement
  SELECT id, customer_id, amount FROM shop.orders WHERE date_trunc($1, created_at) = $2
  with the --bind values substituted:
  SELECT id, customer_id, amount FROM shop.orders WHERE date_trunc('day', created_at) =
  '2025-03-01'::date
  estimated plan, total cost 14272

What the plan shows
  [high] seq_scan on orders
      … (shortened here)

Proposed from the plan
  1. function_wrap (confidence high)
     unwrap DATE_TRUNC('day') equality to a sargable range predicate so PostgreSQL can prune
     partitions and use indexes on the column
     rewrite:
       SELECT id, customer_id, amount FROM shop.orders WHERE created_at >= '2025-03-01'::date
       AND created_at < '2025-03-02'::date

Proof (same rows, then faster)
#  change         rows  before    after   speedup  est. cost      verdict
-  -------------  ----  --------  ------  -------  -------------  -------
1  function_wrap  1096  101.6 ms  1.2 ms  87.0x    14272 -> 3099  Proven
  1: same rows, 86.96x faster (recorded by the database)

Verdict
  1 proposal(s) proven: same rows and at least 1.2x faster. A person still decides what to deploy.
  (Proven 1)
  note: Index orders_created_idx looks unused only because this statement cannot use it (its
        filter wraps the column). A proposed rewrite does use it, so the finding and its DROP
        INDEX suggestion were withheld.

Evidence is in the ledger: pqn evidence 1
```

| Verdict | Meaning |
|---|---|
| `Proven` | Same rows (a fingerprint of every row) and at least 1.2 times faster in the measured run |
| `NotFaster` | Same rows, not 1.2 times faster |
| `Different` | The rows differ. Never an improvement, however fast |
| `Unverified` | Not measured: `$n` placeholders, a statement timeout, or index DDL, which is review only |

`Proven` is a verification on today's data, not a mathematical proof; see [Verify result equivalence](../workflows/verify-results.md).

A proof `prove` computes in the database carries `"source": "database"`. A proof stored from the client with `record_evidence`, as the replica flow does, is stamped `"source": "client"` whatever its payload says, so one cannot be passed off as the other.
Nothing was created or changed in your database.

A proof runs each statement three times (one fingerprint, two timed runs), so prove statements you would be willing to run. Timings are server-side, planning plus execution, and use parallel workers as your application does; they leave out the time to send rows to the client.

## 5. Check a rewrite of your own

This one covers two days instead of one. It is fast and wrong, so it is `Different` and the exit code is 2:

```bash
pqn prove \
  --before "SELECT id FROM shop.orders WHERE date_trunc('day', created_at) = '2025-03-01'" \
  --after  "SELECT id FROM shop.orders WHERE created_at >= '2025-03-01' AND created_at < '2025-03-03'" \
  --title "a wrong rewrite" || test $? -eq 2
```

```text
Verdict: Different
  the two statements returned different rows
  rows 2192, before 93.5 ms, after 2.5 ms, 37.1x
  (recorded by the database)
  Evidence: pqn evidence 2
```

## 6. Read the record

Each investigation is recorded under your login. Only you, and DBAs, can read it:

```bash
pqn investigations
pqn evidence 1
```

```text
id  when              who    evidence  title                  statement
--  ----------------  -----  --------  ---------------------  ------------------------------------------------------------
2   2026-09-19 18:28  alice  1         a wrong rewrite        SELECT id FROM shop.orders WHERE date_trunc('day', created_…
1   2026-09-19 18:28  alice  3         daily orders are slow  SELECT id, customer_id, amount FROM shop.orders WHERE date_…
#1 plan by alice at …
#2 findings by alice at …
#3 proof by alice at …
```

## 7. Check the limits and the setup

The database enforces these. The tool also refuses the first two before sending them:

```bash
pqn run --sql "SELECT status, count(*) AS orders FROM orders GROUP BY 1 ORDER BY 1"
pqn run --sql "DELETE FROM orders" || test $? -eq 1
pqn run --sql "SELECT pg_read_file('/etc/passwd')" || test $? -eq 1
pqn run --sql "SELECT notes FROM shop.orders LIMIT 1" || test $? -eq 1
```

```text
status    orders
--------  ------
new       200000
paid      200000
refunded  200000
shipped   200000
(4 row(s))
pqn: the statement is not accepted: only SELECT statements are allowed
pqn: the statement is not accepted: query calls a function that is not allowed
pqn: ERROR: permission denied for schema shop (SQLSTATE 42501)
```

A DBA checks the whole setup with `pqn doctor`, which exits 1 on any blocking finding:

```bash
pg postgres -c "CREATE ROLE carol LOGIN PASSWORD 'carol-pw'"
pg postgres -c "SELECT pqn_api.enroll('carol', 'admin')" >/dev/null
PQN_DSN='postgres://carol:carol-pw@127.0.0.1:5433/app' pqn doctor | tail -1
```

```text
0 blocking, 3 warning(s).
```

The three warnings are expected: PostgreSQL lets every role read `pg_stat_statements` and use `TEMP`, and the lab table uses `full`
scope. See [what to do about each](pqn-installation.md#6-check-the-setup).

## 8. Clean up

```bash
docker rm -f pqn-quickstart
```

## See also

[Install the pqn extension](pqn-installation.md) · [PostgreSQL extensions](../integrations/postgres-extension.md) ·
[Investigate a slow query](../workflows/investigate.md)
