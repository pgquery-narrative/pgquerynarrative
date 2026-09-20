#!/usr/bin/env python3
"""Verify the core pitch of the project through the extension, on heavy data, from every angle.

    "We don't ask you for the rewrite. We propose it from the plan, then prove it."

The tool under test is the pqn extension and the pqn terminal tool. This script does not trust them: every
claim they make is checked against an independent oracle that only uses plain SQL run as a superuser.

  A  Discovery      pqn top ranks statements the way pg_stat_statements does
  B  Findings       the plan findings agree with an independent EXPLAIN
  C  Proposals      every rewrite the engine proposes is sargable and its plan really uses the index
  D  Proof          for EVERY proposal, pqn's verdict agrees with an independent EXCEPT ALL comparison,
                    and its speedup agrees in direction and size with an independent EXPLAIN ANALYZE
  E  Honesty        hand-written WRONG rewrites (off by one, NULL semantics, duplicates, formatting, limits)
                    are never Proven, and equal-but-not-faster rewrites are never called improvements
  F  Robustness     other session time zones, concurrent writes during the proof, repeatability
  G  Safety         nothing in the user's database changed (rows, indexes, tables), no session left behind
  H  Limits         a statement timeout stops a heavy proof, and leaves no query running
  I  Access         what the exposure scopes hide, and what scope full leaks (documented)
  J  Ledger         every investigation is recorded, per user, and cannot be rewritten
  K  Cost           what a proof costs the database

Needs Docker, python3 and bin/pqn (make build-pqn). Takes about five minutes and 2 GB of disk.
Usage: python3 tools/db/verify-pqn-pitch.py        (PQN_PITCH_KEEP=1 leaves the database running)
"""
import json
import os
import re
import statistics
import subprocess
import sys
import threading
import time

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
os.chdir(ROOT)
C = os.environ.get("PQN_PITCH_CONTAINER", "pqn-pitch-db")
PORT = os.environ.get("PQN_PITCH_PORT", "5437")
PG_IMAGE = os.environ.get("PG_IMAGE", "postgres:18")
PQN = os.environ.get("PQN_BIN", os.path.join(ROOT, "bin", "pqn"))
DB = "shopdb"
ORDERS, ITEMS = 5_000_000, 12_000_000
KEEP = os.environ.get("PQN_PITCH_KEEP") == "1"
REUSE = os.environ.get("PQN_PITCH_REUSE") == "1"   # skip the setup and use the database left by a KEEP run
results = []  # (section, label, ok, detail)
section_name = ["-"]


def run(cmd, check=True, env=None, inp=None):
    e = dict(os.environ)
    if env:
        e.update(env)
    p = subprocess.run(cmd, capture_output=True, text=True, env=e, input=inp)
    if check and p.returncode != 0:
        raise RuntimeError(f"{cmd[:6]} failed ({p.returncode}): {p.stderr.strip()[:400]}")
    return p


def psql(sql, user="postgres", db=DB, check=True, env=None):
    cmd = ["docker", "exec", "-i"]
    for k, v in (env or {}).items():
        cmd += ["-e", f"{k}={v}"]
    cmd += [C, "psql", "-X", "-q", "-At", "-v", "ON_ERROR_STOP=1", "-U", user, "-d", db]
    p = run(cmd, check=check, inp=sql)
    return (p.stdout if p.returncode == 0 else p.stdout + p.stderr).strip()


def dsn(user, opts=""):
    pw = {"alice": "alice-pw", "bob": "bob-pw", "carol": "carol-pw", "dave": "dave-pw"}[user]
    return f"postgres://{user}:{pw}@127.0.0.1:{PORT}/{DB}" + (f"?options={opts}" if opts else "")


def pqn(args, user="alice", opts=""):
    p = run([PQN] + args, check=False, env={"PQN_DSN": dsn(user, opts)})
    return p.returncode, p.stdout, p.stderr


def pqn_json(args, user="alice", opts=""):
    code, out, err = pqn(args + ["--json"], user, opts)
    try:
        return code, json.loads(out)
    except json.JSONDecodeError:
        raise RuntimeError(f"pqn {' '.join(args)[:80]} printed no JSON (exit {code}): {out[:200]} {err[:200]}")


def section(name):
    section_name[0] = name
    print(f"\n\033[1m== {name}\033[0m", flush=True)


def check(label, ok, detail=""):
    results.append((section_name[0], label, bool(ok), detail))
    print(f"  {'PASS' if ok else 'FAIL'}  {label}" + (f"  [{detail}]" if detail else ""), flush=True)
    return bool(ok)


# ---- independent oracle: plain SQL as a superuser, nothing of ours -------------------------------

def oracle_equal(a, b, env=None):
    """Multiset equality of two statements, computed with EXCEPT ALL both ways."""
    sql = (f"SELECT (SELECT count(*) FROM (({a}) EXCEPT ALL ({b})) x), (SELECT count(*) FROM (({b}) EXCEPT ALL ({a})) y), "
           f"(SELECT count(*) FROM ({a}) z), (SELECT count(*) FROM ({b}) w)")
    out = psql(sql, env=env).split("|")
    d1, d2, na, nb = (int(v) for v in out)
    return d1 == 0 and d2 == 0, na, nb


def explain_ms(sql, runs=3, env=None):
    """Fastest of `runs` EXPLAIN ANALYZE runs, all in one session so the backend's own caches are warm
    (a new session per run inflates a 3 ms statement by several ms)."""
    stmt = f"EXPLAIN (ANALYZE, TIMING OFF, SUMMARY ON, FORMAT JSON) {sql}"
    out = psql("\n".join([stmt + ";"] * (runs + 1)), env=env)   # the first run warms the session
    docs, dec, i = [], json.JSONDecoder(), 0
    while i < len(out):
        if out[i].isspace():
            i += 1
            continue
        d, i = dec.raw_decode(out, i)
        docs.append(d[0]["Execution Time"])
    return min(docs[1:])


def plan_json(sql):
    return json.loads(psql(f"EXPLAIN (FORMAT JSON) {sql}"))[0]["Plan"]


def plan_text(sql):
    return json.dumps(plan_json(sql))


# ---- setup ---------------------------------------------------------------------------------------

def install_pqn(restart=True):
    # what the user does: files, one restart for pg_stat_statements, CREATE EXTENSION, init
    share = run(["docker", "exec", C, "pg_config", "--sharedir"]).stdout.strip() + "/extension"
    for f in ("pqn.control", "pqn--1.0.sql", "pqn--1.0--1.1.sql"):
        run(["docker", "cp", f"infra/pqn-extension/{f}", f"{C}:{share}/"])
    if restart:
        psql("ALTER SYSTEM SET shared_preload_libraries = 'pg_stat_statements'")
        run(["docker", "restart", C])
        ready()
    psql("CREATE EXTENSION IF NOT EXISTS pg_stat_statements; CREATE EXTENSION pqn;")
    psql("SELECT pqn_api.init()")
    for t, cols in (("customers", "'id','name','region'"), ("orders", "'id','customer_id','created_at','status','total'"),
                    ("order_items", "'id','order_id','product_id','qty'")):
        psql(f"SELECT pqn_api.expose('shop.{t}', ARRAY[{cols}], '{t}', 'full')")
    psql("SELECT pqn_api.expose('shop.secrets', ARRAY['id','owner'], 'secrets', 'view')")
    for u, g, t in (("alice", "analyst", "600s"), ("bob", "viewer", "15s"), ("carol", "admin", "60s"), ("dave", "analyst", "2s")):
        psql(f"DO $$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '{u}') THEN CREATE ROLE {u} LOGIN PASSWORD '{u}-pw'; END IF; END $$; "
             f"SELECT pqn_api.enroll('{u}', '{g}', '{t}')")


def setup():
    section("Setup: a plain PostgreSQL with heavy data, then the extension installed the way the guide says")
    run(["docker", "rm", "-f", C], check=False)
    run(["docker", "run", "-d", "--name", C, "-p", f"127.0.0.1:{PORT}:5432", "-e", "POSTGRES_PASSWORD=secret",
         "-e", f"POSTGRES_DB={DB}", "--shm-size=1g", PG_IMAGE])
    ready()
    t0 = time.time()
    psql(f"""
CREATE SCHEMA shop;
CREATE TABLE shop.customers AS SELECT g AS id, 'customer ' || g AS name,
  CASE WHEN g % 9 = 0 THEN NULL ELSE 'c' || g || '@example.test' END AS email,
  (ARRAY['eu','us','apac','latam'])[1 + g % 4] AS region FROM generate_series(1, 300000) g;
ALTER TABLE shop.customers ADD PRIMARY KEY (id);
CREATE TABLE shop.orders AS SELECT g AS id, (1 + (g::bigint * 7919) % 300000)::int AS customer_id,
  timestamptz '2024-01-01' + ((g::bigint * 13) % 730)::int * interval '1 day' + (g % 86400) * interval '1 second' AS created_at,
  (ARRAY['paid','new','refunded','shipped'])[1 + g % 4] AS status, ((g::bigint * 31) % 99973)::numeric / 100 AS total,
  CASE WHEN g % 7 = 0 THEN NULL WHEN g % 11 = 0 THEN 'none' ELSE 'note ' || g END AS notes
  FROM generate_series(1, {ORDERS}) g;
ALTER TABLE shop.orders ADD PRIMARY KEY (id);
CREATE INDEX orders_created_idx ON shop.orders (created_at);
CREATE TABLE shop.order_items AS SELECT g AS id, (1 + (g::bigint * 7) % {ORDERS})::int AS order_id,
  (1 + (g::bigint * 131) % 20000)::int AS product_id, (1 + g % 5) AS qty FROM generate_series(1, {ITEMS}) g;
ALTER TABLE shop.order_items ADD PRIMARY KEY (id);
-- rows that sit exactly on the edges a rewrite can get wrong, and rows that match two OR branches
INSERT INTO shop.orders (id, customer_id, created_at, status, total, notes) VALUES
  ({ORDERS}+1, 1, '2025-03-01 00:00:00+00', 'paid', 10, NULL), ({ORDERS}+2, 2, '2025-03-01 00:00:00+00', 'paid', 20, 'none'),
  ({ORDERS}+3, 3, '2025-02-28 23:59:59.999999+00', 'paid', 30, 'x'), ({ORDERS}+4, 4, '2025-03-31 23:59:59.999999+00', 'paid', 40, 'x'),
  ({ORDERS}+5, 5, '2025-04-01 00:00:00+00', 'paid', 50, 'x'), ({ORDERS}+6, 4242, '2025-12-31 12:00:00+00', 'paid', 60, NULL),
  ({ORDERS}+7, 6, '2025-03-15 23:59:59.999999+00', 'paid', 70, 'x');
CREATE TABLE shop.secrets AS SELECT g AS id, 'owner ' || (g % 50) AS owner, 'S-' || lpad(g::text, 5, '0') AS ssn FROM generate_series(1, 100000) g;
ALTER TABLE shop.secrets ADD PRIMARY KEY (id);
ANALYZE;""")
    print(f"   database: {psql('select pg_size_pretty(pg_database_size(current_database()))')}, built in {time.time()-t0:.0f}s")
    install_pqn()
    ok = psql("SELECT count(*) FROM pqn_api.verify_setup() WHERE level = 'BLOCK'") == "0"
    check("the setup check finds nothing blocking after the install", ok)


def ready():
    for _ in range(120):
        if run(["docker", "exec", C, "pg_isready", "-U", "postgres", "-h", "127.0.0.1", "-d", DB], check=False).returncode == 0:
            time.sleep(2)
            return
        time.sleep(1)
    raise RuntimeError("database did not start")


# ---- the shapes ------------------------------------------------------------------------------------

SHAPES = [  # (name, sql, kind)  kind: "date" = a function on created_at the engine should unwrap;
#                        "date-wide" = the same, but the range covers half the table, so a seq scan is the right plan
    ("date_trunc month", "SELECT id, total FROM shop.orders WHERE date_trunc('month', created_at) = date '2025-03-01'", "date"),
    ("date_trunc day", "SELECT id, total FROM shop.orders WHERE date_trunc('day', created_at) = '2025-03-01'", "date"),
    ("::date cast", "SELECT id, total FROM shop.orders WHERE created_at::date = date '2025-03-01'", "date"),
    ("to_char", "SELECT id, total FROM shop.orders WHERE to_char(created_at, 'YYYY-MM') = '2025-03'", "date"),
    ("extract year", "SELECT count(*), sum(total) FROM shop.orders WHERE extract(year FROM created_at) = 2025", "date-wide"),
    ("coalesce (NULLs)", "SELECT count(*) FROM shop.orders WHERE coalesce(notes, 'none') = 'none'", "other"),
    ("OR across columns", "SELECT id FROM shop.orders WHERE customer_id = 4242 OR created_at >= '2025-12-30'", "other"),
    ("IN subquery", "SELECT id FROM shop.orders WHERE customer_id IN (SELECT id FROM shop.customers WHERE region = 'eu' AND id < 300)", "other"),
    ("NOT IN with NULLs", "SELECT id FROM shop.customers WHERE email NOT IN (SELECT email FROM shop.customers WHERE id < 40)", "other"),
    ("anti-join", "SELECT c.id FROM shop.customers c LEFT JOIN shop.orders o ON o.customer_id = c.id AND o.created_at >= '2025-12-31' WHERE o.id IS NULL", "other"),
    ("already fine", "SELECT id, status, total FROM shop.orders WHERE id = 12345", "other"),
]
DATE_FUNC_ON_COLUMN = re.compile(r"(date_trunc|to_char|extract|date_part)\s*\([^)]*created_at|created_at\s*::\s*date", re.I)


def workload():
    section("A. Discovery: does pqn top rank statements the way pg_stat_statements does?")
    psql("SELECT pg_stat_statements_reset()")
    scripts = {
        "w1": "\\set m random(0, 23)\nSELECT date_trunc('day', created_at), sum(total) FROM shop.orders WHERE date_trunc('month', created_at) = date '2024-01-01' + (:m * interval '1 month') GROUP BY 1;\n",
        "w2": "\\set c random(1, 300000)\nSELECT id, created_at, total FROM shop.orders WHERE customer_id = :c ORDER BY created_at DESC LIMIT 20;\n",
        "w3": "\\set o random(1, 5000000)\nSELECT product_id, sum(qty) FROM shop.order_items WHERE order_id = :o GROUP BY product_id;\n",
        "w4": "\\set i random(1, 5000000)\nSELECT id, status, total FROM shop.orders WHERE id = :i;\n",
    }
    for n, body in scripts.items():
        run(["docker", "exec", "-i", C, "sh", "-c", f"cat > /tmp/{n}.sql"], inp=body)
    run(["docker", "exec", C, "pgbench", "-n", "-U", "postgres", "-d", DB, "-c", "4", "-j", "4", "-T", "30",
         "-f", "/tmp/w1.sql@1", "-f", "/tmp/w2.sql@3", "-f", "/tmp/w3.sql@3", "-f", "/tmp/w4.sql@20"])
    # the statement the guide uses for the --queryid path, run with different values
    for d in range(1, 7):
        psql(f"SELECT id, total FROM shop.orders WHERE date_trunc('day', created_at) = '2025-03-0{d}'")
    truth = psql("SELECT queryid FROM pg_stat_statements WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database()) "
                 "AND query NOT ILIKE '%pg_stat_statements%' ORDER BY total_exec_time DESC LIMIT 5").split("\n")
    code, top = pqn_json(["top", "-n", "5"])
    got = [str(r["queryid"]) for r in top]
    # pqn top also lists statements from this script's own psql calls; compare with the same filter
    check("pqn top returns rows and exits 0", code == 0 and len(top) == 5)
    check("pqn top's ranking equals pg_stat_statements' own ranking by total time", got == truth, f"{got[:3]} vs {truth[:3]}")
    check("pqn top's mean time equals pg_stat_statements' mean time",
          abs(float(top[0]["mean_ms"]) - float(psql(f"SELECT mean_exec_time FROM pg_stat_statements WHERE queryid = {truth[0]} LIMIT 1"))) < 5)
    return top


def proposals():
    section("B, C, D. Every proposal: findings agree with EXPLAIN, the rewrite is sargable, the verdict agrees with an independent oracle")
    table = []
    for name, sql, kind in SHAPES:
        code, rep = pqn_json(["investigate", "--title", f"pitch: {name}", "--max-proofs", "5", "--sql", sql])
        cands = [c for c in rep["candidates"] if c["kind"] == "sql_rewrite"]
        idx = [c for c in rep["candidates"] if c["kind"] == "index_ddl"]
        base_plan = plan_text(sql)
        # B. findings vs an independent EXPLAIN
        seq = any(f["category"] == "seq_scan" for f in rep["findings"])
        real_seq = "Seq Scan" in base_plan
        if kind.startswith("date"):
            check(f"[{name}] the seq_scan finding matches the independent plan (Seq Scan: {real_seq})", seq == real_seq)
        check(f"[{name}] the exit code follows the verdicts (0 iff something is Proven)",
              (code == 0) == (rep["proven"] > 0), f"exit {code}, proven {rep['proven']}")
        if not cands:
            table.append((name, "no rewrite", "-", "-", "-", "-"))
            check(f"[{name}] no proposal is an honest answer here", True, f"{len(idx)} index proposal(s)" if idx else "nothing to propose")
            continue
        for c in cands:
            cs = c["sql"]
            v = c["verdict"]
            eq, na, nb = oracle_equal(sql, cs)
            # D. the verdict against the oracle
            if v in ("Proven", "NotFaster"):
                check(f"[{name}] {c['category']}: {v} means the rows really are equal (oracle: {na} = {nb} rows)", eq)
            elif v == "Different":
                check(f"[{name}] {c['category']}: Different means the rows really differ (oracle: {na} vs {nb})", not eq)
            else:
                check(f"[{name}] {c['category']}: {v} is not a claim about the rows", True, c.get("reason", "")[:70])
            # C. sargable, and the plan uses the index
            if kind.startswith("date"):
                check(f"[{name}] the proposed SQL no longer wraps created_at in a function", not DATE_FUNC_ON_COLUMN.search(cs))
            if kind == "date":
                check(f"[{name}] the proposal's real plan uses orders_created_idx", "orders_created_idx" in plan_text(cs))
            # D. independent timing
            if v in ("Proven", "NotFaster") and c.get("before_ms"):
                i_before, i_after = explain_ms(sql), explain_ms(cs)
                i_speed = i_before / i_after if i_after else 0
                p_speed = c["speedup"]
                same_dir = (i_speed > 1) == (p_speed > 1.0) or abs(i_speed - 1) < 0.15
                # under 1 ms a ratio is timer noise (0.006 ms against 0.001 ms is '6x'): direction only
                near = p_speed == 0 or min(i_after, c["after_ms"]) < 1 or 1 / 3 <= (i_speed / p_speed) <= 3
                check(f"[{name}] speedup: pqn {p_speed:.1f}x, independent EXPLAIN ANALYZE {i_speed:.1f}x (same direction, within 3x)",
                      same_dir and near)
                table.append((name, c["category"], v, f"{c['before_ms']:.0f}", f"{c['after_ms']:.0f}", f"{p_speed:.1f}x / {i_speed:.1f}x"))
            else:
                table.append((name, c["category"], v, "-", "-", "-"))
    print("\n   shape                 change            verdict     before ms  after ms  speedup (pqn / independent)")
    for r in table:
        print(f"   {r[0]:<21} {r[1]:<16}  {r[2]:<10}  {r[3]:>9}  {r[4]:>8}  {r[5]}")
    # the queryid path, with binds, on a statement pg_stat_statements recorded
    code, top = pqn_json(["top", "-n", "30"])
    qid = next((r["queryid"] for r in top if "date_trunc" in r["query"] and "$2" in r["query"] and "$3" not in r["query"] and "sum" not in r["query"]), None)
    if check("the --queryid path finds the recorded statement", qid is not None):
        code, rep = pqn_json(["investigate", "--queryid", str(qid), "--bind", "day", "--bind", "2025-03-04", "--title", "pitch: queryid path"])
        c = next((x for x in rep["candidates"] if x["kind"] == "sql_rewrite"), None)
        check("--queryid with --bind proposes and proves a rewrite", c is not None and c["verdict"] == "Proven",
              c["verdict"] if c else "no candidate")
        if c:
            eq, _, _ = oracle_equal(rep["executed_sql"], c["sql"])
            check("and the independent oracle agrees the rows are equal", eq)


def honesty():
    section("E. Honesty: wrong rewrites are never Proven, and equal-but-not-faster is never an improvement")
    base = "SELECT id FROM shop.orders WHERE date_trunc('month', created_at) = date '2025-03-01'"
    right = "SELECT id FROM shop.orders WHERE created_at >= '2025-03-01' AND created_at < '2025-04-01'"
    cases = [
        ("the correct range", base, right, "equal"),
        ("ends one second early", base, "SELECT id FROM shop.orders WHERE created_at >= '2025-03-01' AND created_at < '2025-03-31 23:59:59'", "different"),
        ("includes the next month's first instant", base, "SELECT id FROM shop.orders WHERE created_at >= '2025-03-01' AND created_at <= '2025-04-01'", "different"),
        ("starts one instant late", base, "SELECT id FROM shop.orders WHERE created_at > '2025-03-01' AND created_at < '2025-04-01'", "different"),
        ("two months", base, "SELECT id FROM shop.orders WHERE created_at >= '2025-03-01' AND created_at < '2025-05-01'", "different"),
        ("another time zone's month", base, "SELECT id FROM shop.orders WHERE created_at >= timestamptz '2025-03-01' AT TIME ZONE 'Asia/Kolkata' AND created_at < timestamptz '2025-04-01' AT TIME ZONE 'Asia/Kolkata'", "different"),
        ("COALESCE that drops the NULLs", "SELECT count(*) FROM shop.orders WHERE coalesce(notes, 'none') = 'none'", "SELECT count(*) FROM shop.orders WHERE notes = 'none'", "different"),
        ("COALESCE, correctly", "SELECT count(*) FROM shop.orders WHERE coalesce(notes, 'none') = 'none'", "SELECT count(*) FROM shop.orders WHERE notes = 'none' OR notes IS NULL", "equal"),
        ("UNION ALL that duplicates rows", "SELECT id FROM shop.orders WHERE customer_id = 4242 OR created_at >= '2025-12-30'",
         "SELECT id FROM shop.orders WHERE customer_id = 4242 UNION ALL SELECT id FROM shop.orders WHERE created_at >= '2025-12-30'", "different"),
        ("UNION (removes the duplicates)", "SELECT id FROM shop.orders WHERE customer_id = 4242 OR created_at >= '2025-12-30'",
         "SELECT id FROM shop.orders WHERE customer_id = 4242 UNION SELECT id FROM shop.orders WHERE created_at >= '2025-12-30'", "equal"),
        ("NULL-blind inequality", "SELECT id FROM shop.customers WHERE email <> 'x'", "SELECT id FROM shop.customers WHERE email IS DISTINCT FROM 'x'", "different"),
        ("a LIMIT added", "SELECT id FROM shop.orders WHERE status = 'paid'", "SELECT id FROM shop.orders WHERE status = 'paid' LIMIT 100", "different"),
        ("a value changed", "SELECT id, total FROM shop.orders WHERE id < 1000", "SELECT id, total * 2 FROM shop.orders WHERE id < 1000", "different"),
        ("only the formatting changed", "SELECT id, total FROM shop.orders WHERE id < 1000", "SELECT id, total::numeric(12,1) FROM shop.orders WHERE id < 1000", "different"),
        ("only the row order changed", "SELECT id FROM shop.orders WHERE id < 20000 ORDER BY id", "SELECT id FROM shop.orders WHERE id < 20000 ORDER BY id DESC", "equal"),
        ("identical statements", base, base, "equal"),
        ("the same rows, slower", right, base, "equal"),
    ]
    for name, a, b, want in cases:
        code, rep = pqn_json(["prove", "--title", f"pitch: {name}", "--before", a, "--after", b])
        c = rep["candidates"][0]
        v = c["verdict"]
        eq, na, nb = oracle_equal(a, b)
        check(f"[{name}] the oracle agrees with the expectation ({want})", eq == (want == "equal"), f"oracle rows {na}/{nb}")
        if want == "different":
            check(f"[{name}] is Different, never Proven", v == "Different" and code == 2, f"{v}, exit {code}")
        else:
            check(f"[{name}] is a claim about equal rows, not Different", v in ("Proven", "NotFaster"), v)
    # equal but slower must not be an improvement
    code, rep = pqn_json(["prove", "--before", right, "--after", base])
    check("equal but slower is NotFaster, exit 2", rep["candidates"][0]["verdict"] == "NotFaster" and code == 2)
    code, rep = pqn_json(["prove", "--before", base, "--after", base])
    check("identical statements are not called an improvement", rep["candidates"][0]["verdict"] == "NotFaster")


def robustness():
    section("F. Robustness: other time zones, concurrent writes during a proof, repeatability")
    sql = "SELECT id FROM shop.orders WHERE created_at::date = date '2025-03-01'"
    for tz in ("UTC", "America/New_York", "Asia/Kolkata", "Pacific/Auckland"):
        opts = "-c%20timezone%3D" + tz.replace("/", "%2F")
        code, rep = pqn_json(["investigate", "--no-record", "--sql", sql], opts=opts)
        c = next((x for x in rep["candidates"] if x["kind"] == "sql_rewrite"), None)
        env = {"PGOPTIONS": f"-c timezone={tz}"}
        if c:
            eq, na, nb = oracle_equal(sql, c["sql"], env=env)
            check(f"[time zone {tz}] the ::date rewrite is {c['verdict']}, and the oracle in that time zone agrees ({na} rows)",
                  c["verdict"] == "Proven" and eq)
        else:
            check(f"[time zone {tz}] a proposal was made", False)
    # concurrent writers inserting rows INTO the tested day while the proof runs
    stop = threading.Event()

    def writer():
        n = 0
        while not stop.is_set():
            psql(f"INSERT INTO shop.orders (id, customer_id, created_at, status, total) VALUES ({ORDERS + 1000 + n}, 7, "
                 f"timestamptz '2025-03-01 12:00:00+00' + {n % 500} * interval '1 second', 'paid', 1)", check=False)
            n += 1
    t = threading.Thread(target=writer)
    t.start()
    try:
        before = "SELECT id FROM shop.orders WHERE created_at::date = date '2025-03-01'"
        after = "SELECT id FROM shop.orders WHERE created_at >= '2025-03-01' AND created_at < '2025-03-02'"
        verdicts = []
        for _ in range(4):
            code, rep = pqn_json(["prove", "--no-record", "--before", before, "--after", after])
            verdicts.append(rep["candidates"][0]["verdict"])
    finally:
        stop.set()
        t.join()
    check("while rows are inserted into the tested day, an equal pair is never Different (both sides see one snapshot)",
          "Different" not in verdicts, str(verdicts))
    written = psql(f"SELECT count(*) FROM shop.orders WHERE id >= {ORDERS + 1000}")
    check(f"the writer really was inserting concurrently ({written} rows)", int(written) > 5)
    # repeatability
    speeds, vs = [], []
    for _ in range(5):
        code, rep = pqn_json(["prove", "--no-record", "--before", before, "--after", after])
        c = rep["candidates"][0]
        vs.append(c["verdict"])
        speeds.append(c["speedup"])
    check("five proofs of the same pair give the same verdict", len(set(vs)) == 1, str(set(vs)))
    spread = max(speeds) / min(speeds)
    check(f"and the speedup stays clearly above the 1.2x bar and within 3x of itself (min {min(speeds):.1f}x, max {max(speeds):.1f}x, spread {spread:.2f}x)",
          min(speeds) >= 1.2 and spread < 3.0)
    print("   (the size of a speedup is an indicator, not a constant: it moves with cache state, the verdict does not)")


SNAP = """
SELECT 'tuples ' || relname || ' ' || n_tup_ins || '/' || n_tup_upd || '/' || n_tup_del FROM pg_stat_user_tables WHERE schemaname = 'shop' AND relname <> 'orders' ORDER BY 1;
SELECT 'index ' || schemaname || '.' || indexname FROM pg_indexes WHERE schemaname NOT LIKE 'pg_%' AND schemaname NOT IN ('information_schema') AND schemaname NOT LIKE 'pqn%' ORDER BY 1;
SELECT 'relation ' || n.nspname || '.' || c.relname || ' ' || c.relkind::text FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname NOT LIKE 'pg_%' AND n.nspname NOT IN ('information_schema') AND n.nspname NOT LIKE 'pqn%' ORDER BY 1;
SELECT 'function ' || n.nspname || '.' || p.proname FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace WHERE n.nspname NOT LIKE 'pg_%' AND n.nspname NOT IN ('information_schema') AND n.nspname NOT LIKE 'pqn%' ORDER BY 1;
SELECT 'privilege ' || relname || ' ' || coalesce(relacl::text, '') FROM pg_class WHERE relnamespace = 'shop'::regnamespace AND relkind = 'r' ORDER BY 1;
"""


def snapshot():
    time.sleep(2)  # statistics are flushed asynchronously
    return psql(SNAP)


def safety(before):
    section("G. Safety: nothing in the user's database changed")
    time.sleep(2)
    after = snapshot()
    diff = [l for l in set(after.split("\n")) ^ set(before.split("\n"))]
    # the exposure grants to pqn_owner are the one intended change to privileges: compare without them
    diff = [l for l in diff if "pqn_owner" not in l and "pqn_reader" not in l]
    check("rows inserted, updated or deleted in customers, order_items, secrets: none", not [d for d in diff if d.startswith("tuples")], str([d for d in diff if d.startswith("tuples")][:3]))
    check("no index was created or dropped", not [d for d in diff if d.startswith("index")], str([d for d in diff if d.startswith("index")][:3]))
    check("no table, view or sequence was created or dropped outside pqn's schemas", not [d for d in diff if d.startswith("relation")])
    check("no function was created outside pqn's schemas", not [d for d in diff if d.startswith("function")])
    idle = psql("SELECT count(*) FROM pg_stat_activity WHERE application_name = 'pqn' AND state <> 'idle'")
    check("no pqn session is running anything afterwards", idle == "0")
    writes = psql("SELECT count(*) FROM pg_stat_statements WHERE query ~* '^\\s*(insert|update|delete|create|drop|alter|truncate)' AND query !~* 'pqn_ledger|pqn\\.' AND query !~* 'pg_stat_statements'")
    print(f"   (statements of that kind seen by pg_stat_statements from any session: {writes}; the load generator and this script account for them)")


def limits():
    section("H. Limits: a statement timeout stops a heavy proof and leaves nothing running")
    heavy = "SELECT count(*) FROM shop.order_items a JOIN shop.order_items b ON a.order_id = b.order_id"
    t0 = time.time()
    code, out, err = pqn(["prove", "--no-record", "--before", heavy, "--after", heavy + " WHERE true"], user="dave")
    took = time.time() - t0
    check("dave's 2-second statement_timeout stops the proof: Unverified with the reason, exit 2 (nothing proven)",
          code == 2 and "Unverified" in out and "statement timeout" in (out + err), f"{took:.1f}s, exit {code}")
    check("and it stops promptly (under 15 s)", took < 15)
    time.sleep(2)
    left = psql("SELECT count(*) FROM pg_stat_activity WHERE usename = 'dave' AND state <> 'idle'")
    check("no query of dave's is still running in the database", left == "0")
    code, out, err = pqn(["run", "--sql", "SELECT pg_sleep(30)"], user="dave")
    check("a hand-written slow statement is stopped too, or refused before it starts", code == 1)


def access():
    section("I. Access: what the exposure scopes hide, and what scope full leaks")
    # start from scope view, whatever a previous run left
    if psql("SELECT count(*) FROM pqn_api.exposed() WHERE view_name = 'secrets'") == "1":
        psql("SELECT pqn_api.unexpose('secrets')")
    psql("SELECT pqn_api.expose('shop.secrets', ARRAY['id','owner'], 'secrets', 'view')")
    view_try = psql("SELECT pqn_api.measure_pair($q$SELECT id FROM shop.secrets WHERE ssn = 'S-00042'$q$, $q$SELECT id FROM shop.secrets WHERE false$q$)", user="alice", check=False)
    check("scope view: a predicate on the hidden column is refused (permission denied)", "permission denied" in view_try, view_try[:70])
    code, out, err = pqn(["run", "--sql", "SELECT ssn FROM secrets"])
    check("scope view: run cannot read the hidden column", code == 1)
    psql("SELECT pqn_api.unexpose('secrets')")
    psql("SELECT pqn_api.expose('shop.secrets', ARRAY['id','owner'], 'secrets', 'full')")
    full_try = psql("SELECT pqn_api.measure_pair($q$SELECT id FROM shop.secrets WHERE ssn = 'S-00042'$q$, $q$SELECT id FROM shop.secrets WHERE false$q$)->>'equal'", user="alice")
    row_exists = psql("SELECT pqn_api.measure_pair($q$SELECT id FROM shop.secrets WHERE ssn = 'S-00042'$q$, $q$SELECT id FROM shop.secrets WHERE false$q$)->'before'->>'rows'", user="alice")
    check("scope full: the documented cost is real. A row count over a hidden column reveals whether a value exists", full_try == "false" and row_exists == "1", f"equal={full_try}, rows={row_exists}")
    run_try = pqn(["run", "--sql", "SELECT ssn FROM secrets"])
    check("scope full: run still cannot return the hidden column", run_try[0] == 1)
    warn = psql("SELECT count(*) FROM pqn_api.verify_setup() WHERE check_name = 'table exposed with scope full' AND detail LIKE '%secrets%'", user="carol")
    check("scope full: the setup check warns about that table by name", warn == "1")
    zero_before = psql("SELECT count(*) FROM shop.orders WHERE total = 0")   # the generated data has some
    for stmt in ("DELETE FROM secrets", "UPDATE orders SET total = 0"):
        code, out, err = pqn(["run", "--sql", stmt])
        check(f"a write is refused: {stmt}", code == 1)
    check("and nothing was written (same number of orders with total 0 as before)",
          psql("SELECT count(*) FROM shop.orders WHERE total = 0") == zero_before)
    check("a viewer cannot investigate", pqn(["investigate", "--no-record", "--sql", "SELECT 1 FROM shop.orders LIMIT 1"], user="bob")[0] == 1)


def ledger():
    section("J. Ledger: every investigation is recorded, per user, and cannot be rewritten")
    n = int(psql("SELECT count(*) FROM pqn_ledger.investigations WHERE title LIKE 'pitch:%'"))
    code, rows = pqn_json(["investigations", "-n", "5000"])
    mine = [r for r in rows if r["title"].startswith("pitch:")]
    check(f"alice sees exactly the investigations recorded ({len(mine)} of {n})", len(mine) == n and n > 20)
    check("every one is attributed to alice", all(r["who"] == "alice" for r in mine))
    kinds = psql("SELECT string_agg(DISTINCT kind, ',' ORDER BY kind) FROM pqn_ledger.evidence")
    check("the evidence kinds are plan, findings and proof", kinds == "findings,plan,proof", kinds)
    proofs = int(psql("SELECT count(*) FROM pqn_ledger.evidence WHERE kind = 'proof'"))
    verdict_mismatch = psql("SELECT count(*) FROM pqn_ledger.evidence WHERE kind = 'proof' AND payload->>'verdict' NOT IN ('Proven','NotFaster','Different','Unverified')")
    check(f"all {proofs} recorded proofs carry a valid verdict", verdict_mismatch == "0")
    check("bob (viewer) sees none of them", pqn_json(["investigations"], user="bob")[1] in ([], None))
    check("carol (admin) sees them all", len([r for r in pqn_json(["investigations", "-n", "5000"], user="carol")[1] if r["title"].startswith("pitch:")]) == n)
    for stmt in ("UPDATE pqn_ledger.evidence SET payload = '{}'", "DELETE FROM pqn_ledger.investigations", "TRUNCATE pqn_ledger.evidence"):
        out = psql(stmt, user="alice", check=False)
        check(f"history cannot be rewritten: {stmt}", "permission denied" in out)
    one = next(r for r in mine if "date_trunc month" in r["title"])
    code, ev = pqn_json(["evidence", str(one["id"])])
    kinds_one = sorted(e["kind"] for e in ev)
    check("a recorded investigation can be read back with its plan, findings and proof", kinds_one[0] == "findings" and "plan" in kinds_one and "proof" in kinds_one, str(kinds_one))


def cost():
    section("K. Cost: what a proof costs the database")
    # pg_stat_statements only sees top-level statements by default; count the ones inside the function
    psql("ALTER SYSTEM SET pg_stat_statements.track = 'all'; SELECT pg_reload_conf()")
    time.sleep(1)
    try:
        psql("SELECT pg_stat_statements_reset()")
        a = "SELECT id FROM shop.orders WHERE date_trunc('month', created_at) = date '2025-06-01'"
        b = "SELECT id FROM shop.orders WHERE created_at >= '2025-06-01' AND created_at < '2025-07-01'"
        pqn(["prove", "--no-record", "--before", a, "--after", b])
        fp = psql("SELECT coalesce(sum(calls), 0) FROM pg_stat_statements WHERE query LIKE '%hashtextextended%'")
        timed = psql("SELECT coalesce(sum(calls), 0) FROM pg_stat_statements WHERE query LIKE 'EXPLAIN (ANALYZE, TIMING OFF%'")
    finally:
        psql("ALTER SYSTEM RESET pg_stat_statements.track; SELECT pg_reload_conf()")
    check("one proof runs each statement once to fingerprint it and twice to time it: 2 + 4 executions", (fp, timed) == ("2", "4"),
          f"{fp} fingerprint runs, {timed} timed runs")
    print("   (a proof is the workload it measures, about three times over: prove statements you would be willing to run)")


def teardown():
    if KEEP or REUSE:
        print(f"\n   the database is left running: container {C}, port {PORT}, database {DB} (postgres/secret, alice/alice-pw, carol/carol-pw)")
        return
    run(["docker", "rm", "-f", C], check=False)


def main():
    if not os.access(PQN, os.X_OK):
        sys.exit("build the tool first: make build-pqn")
    try:
        if REUSE:
            # keep the heavy data, install the extension files as they are on disk now
            psql("DROP EXTENSION IF EXISTS pqn; DROP SCHEMA IF EXISTS pqn CASCADE")
            install_pqn(restart=False)
        else:
            setup()
        top = workload()
        snap0 = snapshot()
        proposals()
        honesty()
        safety(snap0)          # after the read-only work, before the tests that deliberately write
        robustness()
        limits()
        access()
        ledger()
        cost()
    finally:
        teardown()
    failed = [r for r in results if not r[2]]
    print("\n" + "=" * 70)
    by = {}
    for s, l, ok, d in results:
        by.setdefault(s.split(":")[0], [0, 0])[0 if ok else 1] += 1
    for s, (p, f) in by.items():
        print(f"  {s:<12} pass={p:<3} fail={f}")
    print(f"RESULT pqn pitch: pass={len(results) - len(failed)} fail={len(failed)}")
    for r in failed:
        print(f"  FAILED [{r[0][:40]}] {r[1]} {r[3]}")
    sys.exit(1 if failed else 0)


if __name__ == "__main__":
    main()
