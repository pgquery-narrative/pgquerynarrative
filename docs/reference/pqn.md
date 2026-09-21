# pqn reference

Facts about the `pqn` extension and its terminal tool, for lookup. To try it, see the
[quick start](../getting-started/pqn-extension.md); to install it on your own server, see
[Install the pqn extension](../getting-started/pqn-installation.md). `pqn` runs against PostgreSQL directly and needs
no PgQueryNarrative server. It is not the [`pgquerynarrative` CLI](cli.md), and it is a different extension from
[`pgquerynarrative`](../integrations/postgres-extension.md), which calls the REST API.

## The tool

`pqn <command> [flags]`. It logs in as you and stores no secret. Flags may come before or after the statement. A statement that contains a word starting with `-` goes in quotes, or after `--`; `pqn run SELECT -1` and a `--` comment after a word are read as SQL.

| Command | What it does | Flags it reads |
|---|---|---|
| `doctor` | Runs `pqn_api.verify_setup()`. Exits 1 on any blocking finding | |
| `top` | The statements that cost the most, from `pg_stat_statements` | `-n` |
| `plan` | Estimated plan and findings for one statement. Reads only, keeps nothing. A statement that writes cannot be planned: plan its SELECT | statement, `--bind` |
| `run` | Runs one read-only statement over the views you may see | statement, `-n` |
| `investigate` | Plans it, names what is wrong, proposes fixes from the plan and proves them | statement, `--queryid`, `--bind`, `--title`, `--no-record`, `--max-candidates`, `--max-proofs` |
| `prove` | Proves a rewrite you wrote: same rows, then faster | `--before`, `--after`, `--bind`, `--title`, `--no-record`, `--id` |
| `investigations` | Your recorded investigations | `-n` |
| `evidence <id>` | The evidence recorded for one investigation | `--id` |
| `version` | | |

| Flag | Meaning |
|---|---|
| `--dsn` | Primary server. Default `$PQN_DSN`, then the libpq environment (`PGHOST`, `PGUSER`, `PGSERVICE`, `~/.pgpass`) |
| `--replica` | Optional second server. Default `$PQN_REPLICA_DSN`. Reads (plan, run, measuring) use it when set; the ledger and the workload statistics use the primary |
| `--json` | Print JSON with `snake_case` field names |
| `--timeout` | Give up after this long. Default 10 minutes |
| `-n` | Rows to show (`top`, `run`, `investigations`). Default 20 |
| statement | `--sql`, `--file`, or the words after the command. Give it once; giving it twice is an error |
| `--bind` | A value for `$1`, `$2`, … Repeat the flag once per placeholder. Without binds a statement with placeholders is planned but not executed |
| `--queryid` | Investigate the statement `pqn top` showed under this id |
| `--title` | A title for the ledger |
| `--no-record` | Write nothing to the ledger |
| `--max-candidates` | Most proposals to consider. Default 5 |
| `--max-proofs` | Most proposals to measure. Default 3 |
| `--before`, `--after` | The original and the rewritten statement (`prove`) |
| `--id` | The investigation to add the proof to (`prove`), or to show (`evidence`) |

**Exit codes.** `0` done; for `investigate` and `prove` it means a proposal was proven. `2` nothing was proven (the
rewrite returned different rows, or was not fast enough). `1` an error, including a refused statement.

## Verdicts

| Verdict | Rule |
|---|---|
| `Proven` | Same rows (an order-independent fingerprint of every row) and at least 1.2 times faster |
| `NotFaster` | Same rows, not 1.2 times faster |
| `Different` | The rows differ. Never an improvement, however fast |
| `Unverified` | Not compared: `$n` placeholders, a statement timeout, index DDL (review only), or both statements returned no rows. Two empty results are equal whatever the statements do |

A verification on today's data, not a mathematical proof: the fingerprint is 128 bits (two independent 64-bit hashes), so agreement is probabilistic and not built to resist someone crafting colliding rows. See [Verify result equivalence](../workflows/verify-results.md),
which uses the same fingerprint. Times are server-side, planning plus execution, and are the fastest of two rounds
that alternate the two statements. A proof `prove()` computes carries `"source": "database"`; one stored with
`record_evidence`, as the replica flow does, carries `"source": "client"` whatever its payload says.

## SQL API

Everything is in schema `pqn_api`. Each group includes the ones below it: `pqn_admin` ⊃ `pqn_analyst` ⊃ `pqn_viewer`.
Nothing is executable by `PUBLIC`.

| Function | Group | Returns and notes |
|---|---|---|
| `pqn_api.top(n = 20)` | analyst | Statements by total time: `queryid`, `query`, `calls`, `total_exec_time`, `mean_exec_time`, `rows` |
| `pqn_api.plan(query)` | analyst | The estimated plan as JSON. Read only, and nothing it does is kept. A statement that writes (INSERT, UPDATE, DELETE, MERGE) is refused with a hint to plan its SELECT. `$n` placeholders need PostgreSQL 16 |
| `pqn_api.run(query, row_limit = 100)` | analyst | `{rows, columns, truncated}`. One read-only statement over the exposed views; `row_limit` is clamped to 1–10000. Makes the rest of its transaction read only |
| `pqn_api.findings(plan)` | analyst | Findings from a plan, as rules over the plan JSON |
| `pqn_api.measure_pair(a, b, repeats = 2)` | analyst | `{equal, before, after, speedup, rounds}`. Both fingerprints in one snapshot; `repeats` is clamped to 1–5. Refuses `$n`. Runs the statements read only, so one that writes fails, and leaves your transaction writable |
| `pqn_api.investigate(query, title, queryid)` | analyst | Plans, finds, and records a new investigation. `investigation_id` and `findings` in the result |
| `pqn_api.prove(investigation, before, after, note)` | analyst | The verdict, both plan costs and the measurement, recorded as a proof |
| `pqn_api.record_investigation(query, queryid, title)` | analyst | The new investigation's id |
| `pqn_api.record_evidence(investigation, kind, payload)` | analyst | The new evidence id. `kind` is lowercase letters and underscores, at most 40; the payload at most 1 MiB. Your own investigation only; an admin may write to anyone's |
| `pqn_api.investigations(limit = 50)` | viewer | Your investigations; an admin sees everyone's |
| `pqn_api.evidence(investigation)` | viewer | Its evidence, under the same rule |
| `pqn_api.verify_setup()` | admin | One row per check: `level` (`BLOCK`, `WARN`, `INFO`, `OK`), `check_name`, `detail`, `fix` |
| `pqn_api.init()` | admin | Creates the ledger, the exposure registry and the limits registry. Safe to repeat |
| `pqn_api.expose(table, columns, view_name, scope = 'view')`, `pqn_api.expose_sql(…)` | admin | Creates the view and registers it; `expose_sql` prints the statements instead. `scope` is `view` or `full` |
| `pqn_api.unexpose(view_name)`, `pqn_api.exposed()` | admin | Removes a view and leaves `pqn_owner` exactly the access the remaining views on that table need; lists what is exposed |
| `pqn_api.enroll(login, group = 'analyst', stmt_timeout = '15s')`, `pqn_api.enroll_sql(…)` | admin | Puts a login into `viewer`, `analyst` or `admin` with limits; `enroll_sql` prints the statements. The timeout needs a unit: `15s`, `500ms` or `2min` |
| `pqn_api.enforce_limits()` | admin | Cancels statements that outlived their enrolled limit. Needs a superuser, or `pg_read_all_stats` and `pg_signal_backend`. A superuser's session cannot be cancelled without being one: it is reported with `cancelled = false` and the pass goes on |
| `pqn_api.record_limit(login, ms)` | admin | Called by the `enroll` script |

`explain_ms` and `exposed_path` are helpers only their owner can execute.

The **Group** column is who may *execute* a function. Some admin functions also change roles and grants, and a caller that is not a
superuser needs those rights too. Membership in `pqn_admin` alone is enough for the read-only ones:

| Function | What a non-superuser needs beyond `pqn_admin` |
|---|---|
| `verify_setup()`, `exposed()`, `record_limit()`, `expose_sql()`, `enroll_sql()` | Nothing. The two `_sql` functions only print statements |
| `init()` | Membership in `pqn_owner` and `pqn_ledger`: the installer, not a day-to-day administrator |
| `expose()`, `unexpose()` | Membership in `pqn_owner` with `SET`, and the right to grant on the table and its schema (own them, or hold `GRANT OPTION`). Otherwise run the printed script as someone who has it |
| `enroll()` | `CREATEROLE`, `ADMIN OPTION` on the group being granted, and `ADMIN` on the login (a login the administrator created has it). Otherwise run the printed script as someone who has it |
| `enforce_limits()` | A superuser, or membership in `pg_read_all_stats` and `pg_signal_backend` |

A superuser needs nothing extra. [Install the pqn extension](../getting-started/pqn-installation.md#install-without-a-superuser)
shows how to set up a non-superuser administrator.

## Roles and tables

| Role | Kind | Holds |
|---|---|---|
| `pqn_viewer`, `pqn_analyst`, `pqn_admin` | Group, granted to people | The function grants above |
| `pqn_owner` | `NOLOGIN` owner | The curated views, `plan`, `measure_pair`, the registries. Reads the exposed columns, or the whole table for scope `full` |
| `pqn_reader` | `NOLOGIN` owner | `run`. Reads only the curated views |
| `pqn_stats` | `NOLOGIN` owner | `top`. Member of `pg_monitor` |
| `pqn_ledger` | `NOLOGIN` owner | The ledger, `investigate`, `prove` and the `record_*` functions |

| Object | What it is |
|---|---|
| `pqn.<view>` | A curated view over one exposed table, with the columns a DBA chose |
| `pqn.exposed` | The exposure registry: schema, table, view, `scope`, columns |
| `pqn.limits` | The statement timeout each person was enrolled with, where they cannot reach it |
| `pqn_ledger.investigations`, `pqn_ledger.evidence` | The audit ledger, attributed to the real login. `DROP EXTENSION` keeps it |
| `pqn_ledger.schema_migrations` | The ledger's version |

Scope `full` lets planning and measuring see every column of the table (row counts can then reveal whether a value
exists), while `run` still returns only the listed columns; `pqn doctor` warns about it. Limits, and why they are
enforced from outside the session, are in [Enforce the limits](../getting-started/pqn-installation.md#enforce-the-limits).
The roles that own things, and why the extension holds no superuser privilege, are in
[Database roles](../security/database-roles.md).

## See also

[Quick start](../getting-started/pqn-extension.md) · [Install the pqn extension](../getting-started/pqn-installation.md) ·
[Investigate a slow query](../workflows/investigate.md) · [Verify result equivalence](../workflows/verify-results.md) ·
[PostgreSQL extensions](../integrations/postgres-extension.md)
