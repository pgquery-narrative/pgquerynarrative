# Verify result equivalence

This is the page to read before trusting an equivalence status. Verification here
means "checked to the extent described below" — never a mathematical proof.

## The five states

| State | Meaning |
|---|---|
| `VerifiedEqual` | Every row of both results contributed to a full-result, order-independent fingerprint, and the fingerprints matched — or, on the fallback path, the whole result was ≤ 1,000 rows on both sides and every row matched |
| `SampleMatch` | Full-result fingerprinting could not run. Row counts matched **and** a bounded, deterministic sample matched. Supporting evidence, not full verification |
| `Different` | Row counts, fingerprints, or the sample disagree |
| `Unverified` | The check could not complete (a query error, a timeout, an unsupported result shape). **Never** reported as a mismatch |
| `NotRequested` | `verify_results` was false |

## How it actually checks

**Primary path — full-result fingerprint.** Both queries run inside one aggregate
query each:

```sql
SELECT count(*)::bigint,
       coalesce(sum(hashtextextended(pgqn_eq::text, 0)::numeric), 0)::text,
       coalesce(bit_xor(hashtextextended(pgqn_eq::text, 0)), 0)::bigint
FROM (<your query>) AS pgqn_eq
```

If both sides' `(count, sum, xor)` triples match, the status is `VerifiedEqual` with
no size cap — this runs the same way whether the result is 10 rows or 10 million.
If the counts, sums or XORs disagree, the status is `Different`.

**Fallback path — when the fingerprint query errors on either side** (a timeout, a
type with no useful text form, or any other execution error): `COUNT(*)` is taken on
both sides; if the counts disagree, `Different`. If they match and the count is
≤ 1,000, the whole result is fetched (unordered) and multiset-compared by hashing
each row's JSON form, sorting the hashes, and including the column names — a match
here is still `VerifiedEqual`. If the count is > 1,000, a deterministic sample of
1,000 rows (`ORDER BY md5(row::text) LIMIT 1000`) is compared the same way; a match
is `SampleMatch`, because 1,000 rows out of a much larger result is not the same
claim as checking every row.

## What the fingerprint does **not** verify

- **Column names.** The primary fingerprint hashes the row's composite text form; it
  does not include column labels.
- **Column types.** Two differently-typed values that render the same text are
  indistinguishable to the hash.
- **Row order.** Both paths are explicitly order-independent. If `ORDER BY` is part
  of your query's contract, check it separately — verification will not catch a
  reordering.

Check those three things yourself when they matter to your query's contract.

## When SQL executes, and what permission it needs

`verify_results: true` always executes both queries — 2 statements on the primary
path, up to 4 on the fallback. It requires the `query` permission on the connection,
in addition to `explain` (or `analyze`, if you also set `analyze: true`). Without
`query`, the call fails before anything runs.

## Acknowledging `SampleMatch`

Because `SampleMatch` is sampled evidence rather than a full check, the UI asks for
explicit confirmation before treating it as shippable, and the API requires the same
thing:

```
POST /investigations/{id}/report?accept_sample_match=true
```

`accept_sample_match` is a **query parameter**, not a body field — this endpoint
takes no request body. A report generated this way is marked `results_sampled` in
its stored provenance, so a reader can tell a sampled result from a fully verified
one.

## The report gate

Once an investigation has a comparison, generating its report enforces:

| Equivalence status | Report allowed? |
|---|---|
| `VerifiedEqual` | Yes |
| `SampleMatch` with `accept_sample_match=true` | Yes, marked `results_sampled` |
| `SampleMatch` without the flag | No — `EQUIVALENCE_SAMPLE_ONLY` |
| `Different`, `Unverified`, `NotRequested` | No — `EQUIVALENCE_NOT_EQUAL` |

**An investigation with no comparison at all can still generate a report** — the gate
only applies once a candidate has been compared. That report carries no equivalence
claim, because none was made.

## Volatile queries

There is no volatility detector: a query using `random()`, `now()`, or anything else
non-deterministic can be run through verification, but it will never come back
`VerifiedEqual` or `SampleMatch` in practice, because the two executions will not
agree. Treat a `Different` result on an intentionally volatile query as expected,
not as a bug in the rewrite.

## See also

[Compare plans](compare.md) · [Evidence and status vocabulary](../reference/evidence.md#result-equivalence) ·
[API errors](../reference/api-errors.md)
