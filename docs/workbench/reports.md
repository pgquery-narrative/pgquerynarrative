# Reports and sharing

## Two report types

| | Investigation report | Workbench report |
|---|---|---|
| Created by | `POST /investigations/{id}/report` | `POST /reports/generate` |
| Content | Deterministic template: plan evidence, before/after SQL, comparison metrics, equivalence status | LLM narrative over query metrics (period comparison, trend, correlations, anomalies), with a deterministic metrics-only fallback if the LLM call fails |
| Gate | Equivalence [gate](../workflows/verify-results.md#the-report-gate) once a candidate exists | None, any successful query can be reported on |
| Where | Investigate flow | Query runner, Ask |

Both are stored the same way and both show up in `GET /reports` /
`GET /reports/{id}` and the **Reports** page.

## Similar reports and rewriting

- `GET /reports/similar?text=…`: semantic search over past reports (needs
  [embeddings](../integrations/semantic-search.md)).
- `POST /reports/rewrite`: takes an existing `report_id` and a plain-language
  `instruction`, and asks the LLM to revise the narrative. Requires an LLM.

## Export

`GET /web/reports/export?id=…` (HTML) and `/web/reports/export/pdf?id=…` (PDF), plus
`/web/reports/export/{md,json,sql}?id=…` for Markdown, raw JSON, and a `.sql` file
with the before/after statements and any candidate index DDL, ready to paste into a
PR or migration.

## Share links

`POST /reports/share` mints a public, unauthenticated link to one report:

- Off by default (`SECURITY_SHARE_LINKS_ENABLED=false`) and **forbidden in
  production StrictMode** until sharing is explicitly hardened for your deployment.
- The token is 24 random bytes, base64url-encoded; only its SHA-256 hash is stored,
  so the raw token exists nowhere in the database.
- Expiry defaults to `SECURITY_SHARE_LINK_DEFAULT_HOURS` (168h = 7 days), up to
  720h (30 days) via `expires_in_hours`.
- By default the shared view hides the underlying SQL; set
  `SECURITY_SHARE_LINK_EXPOSE_SQL=true` to include it.
- `GET /reports/shared/{token}` and `GET /web/reports/export/shared/pdf?token=…` are
  the only routes reachable with no authentication at all; treat a share link as
  exactly as sensitive as the data in the report.
- `GET /reports/{report_id}/shares` lists a report's active links;
  `POST /reports/shares/{id}/revoke` disables one.

## See also

[Verify result equivalence](../workflows/verify-results.md) ·
[Data handling](../security/data-handling.md) · [REST API](../integrations/rest-api.md)
