# RLS multi-tenant demo (`demo.sales`)

Migration `000021_sales_rls_demo` enables **row-level security** on the partitioned
`demo.sales` table. It demonstrates Postgres-native tenant isolation: each sales
rep session sees only its own rows when connected as `pgquerynarrative_sales_rep`
and `app.current_rep` is set.

The **read-only API path is unchanged** — `POST /api/v1/queries/run`,
`/queries/explain`, and `GET /queries/stats` still use `pgquerynarrative_readonly`,
which has a permissive `USING (true)` policy on `demo.sales`.

---

## Roles and which path uses them

| Role | Used by | `demo.sales` visibility |
|------|---------|-------------------------|
| `pgquerynarrative_readonly` | API query pool — run, explain, stats | All rows (policy `sales_select_api_readonly`) |
| `pgquerynarrative_sales_rep` | Manual `psql` demo only (not wired to the app) | Rows where `sales_rep = current_setting('app.current_rep', true)` |
| `pgquerynarrative_app` | App metadata pool — saved queries, reports, embeddings | Owner bypasses RLS (migrations, seed) |
| `postgres` | `make migrate-docker`, admin | Superuser |

Seed data assigns reps from `['A. Lee','B. Singh','C. Patel','D. Kim','E. Garcia']`
(`tools/db/seed-large.sql`).

---

## Policies (on parent `demo.sales`; applies to all partitions)

```sql
-- API read-only path — full visibility
CREATE POLICY sales_select_api_readonly ON demo.sales
    FOR SELECT TO pgquerynarrative_readonly USING (true);

-- Rep-scoped demo
CREATE POLICY sales_select_own_rep ON demo.sales
    FOR SELECT TO pgquerynarrative_sales_rep
    USING (sales_rep = current_setting('app.current_rep', true));
```

`current_setting(..., true)` returns NULL when `app.current_rep` is unset; no rows
match `sales_rep = NULL`, so the rep role sees an empty result set until `SET`.

---

## Prerequisites

```bash
make postgres-up
make migrate-docker    # applies through 000021
make seed-large-docker # optional; needs reps in demo.sales
```

---

## Two-session verify (rep A vs rep B → disjoint results)

**Session A** — only `A. Lee`:

```bash
docker compose exec -T postgres psql -U pgquerynarrative_sales_rep -d pgquerynarrative \
  -c "SET app.current_rep = 'A. Lee'; SELECT DISTINCT sales_rep FROM demo.sales ORDER BY 1;"
```

**Session B** — only `B. Singh`:

```bash
docker compose exec -T postgres psql -U pgquerynarrative_sales_rep -d pgquerynarrative \
  -c "SET app.current_rep = 'B. Singh'; SELECT DISTINCT sales_rep FROM demo.sales ORDER BY 1;"
```

Expect each command to return a single distinct `sales_rep` matching the GUC, and the
two sets to be disjoint.

**Inspect policies** (as superuser):

```bash
docker compose exec -T postgres psql -U postgres -d pgquerynarrative -c '\d+ demo.sales'
```

Look for `Policies:` listing `sales_select_api_readonly` and `sales_select_own_rep`,
and `Row security: enabled`.

**Confirm API path still sees all reps** (readonly role, no GUC):

```bash
docker compose exec -T postgres psql -U pgquerynarrative_readonly -d pgquerynarrative \
  -c "SELECT COUNT(DISTINCT sales_rep) AS rep_count FROM demo.sales;"
```

Expect `5` when seed-large has run (five reps).

---

## Defense in depth (with RLS)

```
Client → App validator → pgquerynarrative_readonly → policy (all rows) → LIMIT/timeout
Manual psql → pgquerynarrative_sales_rep → policy (rep filter) → read-only txn
```

See [Trust model](../trust-model.md) for the
security model.
