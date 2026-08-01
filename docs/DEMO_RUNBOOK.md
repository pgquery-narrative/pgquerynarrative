# Solo full-product demo runbook

Timed walkthrough for a **solo portfolio / hiring demo** (15–20 min core + deep appendix).
Postgres is the hero; AI is optional and governed.

**One-line open:**  
“PgQueryNarrative is Postgres-native query intelligence — safe read-only SQL, EXPLAIN triage, and optional AI narratives — so analytics teams don’t paste production credentials into a chatbot.”

---

## Before you start (5–15 min once)

### Automated path

```bash
# Recommended: product-ready guided demo with small seed.
make demo

# Equivalent lower-level bootstrap:
SEED_LARGE=0 ./tools/demo/bootstrap.sh

# Optional real-world NYC TLC data (downloads months; needs allowlist restart):
WITH_NYC=1 ./tools/demo/bootstrap.sh

# Large benchmark / partition-pruning seed:
make seed-large-docker
```

### Manual checklist

| Step | Command |
|------|---------|
| Stack | `make start-docker` → http://localhost:8080 |
| Large sales | `make seed-large-docker` |
| LLM | Included in compose (`make ollama-up` or `make demo` pulls `llama3.2`) |
| NYC (optional) | Set `DATABASE_ALLOWED_SCHEMAS=demo,opendata`, recreate app, `make seed-nyc-docker MONTHS=2024-01,2024-02,2024-03` |

Verify:

```bash
./tools/demo/smoke_scenes.sh
./tools/demo/multi_org_demo.sh
```

Compose defaults: auth **off** (local only), `LLM_PROVIDER=ollama`, `LLM_BASE_URL=http://ollama:11434` (in-compose Ollama). First run pulls `llama3.2` (~2 GB).

---

## Core arc (15–20 min)

### Scene 0 — Setup check (~30s)

1. Open http://localhost:8080 → **Start guided demo** (or **Investigate**).
2. Confirm the regression inbox and guided scenarios are visible.

**Pitch:** “This starts from workload evidence, not a blank SQL editor. The product flow is regression inbox → plan evidence → candidate comparison → report.”

---

### Scene 1 — Guided investigation (hero, ~5 min)

1. Click **Slow dashboard query**.
2. In **Source query**, point out the anti-pattern:

```sql
SELECT product_category, SUM(total_amount) AS revenue
FROM demo.sales
WHERE DATE_TRUNC('month', date) = DATE '2025-01-01'
GROUP BY product_category
ORDER BY revenue DESC
```

3. In **Execution plan**, show the large total cost and the repeated `Seq Scan`
   findings across partitions.
4. In **Candidate improvement**, keep the prefilled rewrite and click
   **Compare plans**.
5. Review the before/after evidence and improved partition-pruning story.
6. Click **Generate report**. The app navigates directly to the generated
   investigation report.

**Pitch:** “This is the product story: find a regression, inspect evidence, test
the safer rewrite, then hand the team an engineering artifact.”

---

### Scene 2 — Safe query + charts (~2 min)

Open **Workbench** / **Query Runner** and **Run**:

```sql
SELECT product_category, SUM(total_amount) AS total
FROM demo.sales
GROUP BY product_category
ORDER BY total DESC
LIMIT 10;
```

Show: row count, `execution_time_ms`, chart suggestions.

**Pitch:** “Validator allows SELECT/WITH only; timeouts and result caps protect the replica.”

---

### Scene 3 — Ask + narrative (~3 min) — in-compose Ollama

1. In Query Runner, use **Ask in natural language**:  
   `Which product categories drive North region revenue?`
2. Review generated SQL → run → **Generate report** (or Ask→report path).
3. Open **Reports** — headline / takeaways.

**Pitch:** “AI is optional and governed — local Ollama in Docker by default; row samples off (`LLM_SEND_ROW_DATA=false`).”

**If Ask fails:** Run `make ollama-up` (pulls `llama3.2`; first run ~2 GB). Check `docker compose logs ollama`. Host Ollama instead: set `LLM_BASE_URL=http://host.docker.internal:11434` in `.env` and recreate app.

---

### Scene 4 — Save → report → dashboard (~2 min)

1. **Save** Scene 2 SQL with a clear name (`North revenue by category`).
2. From saved query or result, generate a report if not already.
3. **Dashboards** → create dashboard → pin report or saved query widget.

**Pitch:** “Analysts keep the SQL artifact; narratives are attached, not the source of truth.”

---

### Scene 5 — Query Stats (~1 min)

Open **Query Stats** (`/stats`).

**Pitch:** “Close the loop: what actually burned time on the replica (`pg_stat_statements`).”

---

### Scene 6 — Real-world NYC data (~2 min)

Requires `WITH_NYC=1` bootstrap (or manual seed + `DATABASE_ALLOWED_SCHEMAS=demo,opendata`).

```sql
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
```

More queries: `tools/db/opendata-showcase.sql`.

**Pitch:** “Same safety rails on public TLC data — not only synthetic `demo.sales`.”

---

### Scene 7 — Multi-org isolation (~3 min)

```bash
./tools/demo/multi_org_demo.sh
```

Script: creates Org B, seeds Org A–only saved query, proves Org B RLS cannot see it, proves empty/foreign connection allowlist denies access.

UI: org switcher appears when memberships exist (with session/API-key auth). Local compose is auth-off — use the script output as the live proof, then say:

**Pitch:** “Internal multi-org isolation for trusted teams — metadata + connection allowlist fail closed. Not public SaaS signup.”

---

### Scene 8 — Security soundbite (~1 min)

In Query Runner try:

```sql
DELETE FROM demo.sales WHERE id = 1;
```

Expect validation error.

**Pitch:** “Defense in depth: AST validator → readonly role → org RLS on `app.*` → optional Postgres RLS demos (`docs/ops/rls-demo.md`).”

---

## Closing (20s)

“This isn’t a BI suite or an autonomous DBA. It’s a **Postgres-first** control plane for safe analytics: explain what hurts, keep SQL as the artifact, add narrative only when allowed, and isolate orgs on the app side. I run this demo end-to-end alone — same path a trusted internal team would pilot.”

---

## Deep appendix (if they dig in)

| Topic | How |
|-------|-----|
| Index case study | `docs/case-studies/01-query-optimization.md` — covering index story |
| Schedules | Set `SCHEDULE_RUNNER_ENABLED=true`, recreate app, create schedule → **Schedules** runs |
| Share links | `SECURITY_SHARE_LINKS_ENABLED=true`, share a report → `/shared/:token` |
| CLI | `make cli CMD='query "SELECT product_category, COUNT(*) FROM demo.sales GROUP BY 1 LIMIT 5"'` |
| Admin APIs | `POST /api/v1/admin/organizations`, memberships, connection-assignments |
| Pilot gate | `make pilot-acceptance` |
| RLS demo (psql) | `docs/ops/rls-demo.md` — `pgquerynarrative_sales_rep` + `SET app.current_rep` |

---

## Auth / LLM / NYC toggles

| Knob | Local demo default | Notes |
|------|-------------------|--------|
| Auth | Off + `SECURITY_ALLOW_INSECURE_NO_AUTH` | Compose only — never production |
| Allowlist required | Off unless StrictMode | Migration `000044` seeds default org → `default` |
| LLM | Ollama `llama3.2` | Cloud needs `LLM_ALLOW_EXTERNAL_DATA=true` |
| Share links | Off | Enable only for share scene |
| Schedules | Off | Enable only for schedule scene |
| Schemas | `demo` | Add `opendata` for NYC |

---

## Troubleshooting

| Symptom | Fix |
|---------|-----|
| App not ready | `docker compose ps`; `docker compose logs app` |
| Query run/schema: “execution failed” / “pool unavailable” | Stale image — `docker compose build app && docker compose up -d --force-recreate app` (or re-run bootstrap) |
| Guided scenario opens a skeleton forever | Refresh after rebuilding; recent fixes removed the stale `create` loading state and the guided scenario should now open directly |
| Guided scenario says it failed to create | Re-run `make demo`; older builds used stale `sale_date` demo SQL instead of the seeded `date` column |
| EXPLAIN boring | `make seed-large-docker` |
| Ask/report fails | `make ollama-up`; check `docker compose logs ollama`; ensure Docker has ≥6 GB RAM for llama3.2 |
| Investigation report button seems to do nothing | New builds navigate directly to the report page; older builds still create the report, which you can open from **Reports** |
| NYC relation missing | Migrate + `seed-nyc-docker` + allowlist `demo,opendata` + recreate app |
| Multi-org script fails | `make migrate-docker` (need version ≥ 44) |
| Port busy | `make stop` then bootstrap again |
