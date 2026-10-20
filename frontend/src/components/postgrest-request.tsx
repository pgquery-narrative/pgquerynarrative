import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import type { PostgRESTRequest } from "@/api/client";
import { formatFloat } from "@/lib/utils";

// PostgRESTRequestPanel shows the captured request an investigation was
// imported from, the context its replays run in, and what they do not
// reproduce. Clocks stay separate: HTTP, PostgREST transaction and the
// statement's execute phase measure different things.
export function PostgRESTRequestPanel({ request, binds }: { request: PostgRESTRequest; binds?: string[] }) {
  const settings = Object.entries(request.request_settings ?? {}).sort(([a], [b]) => a.localeCompare(b));
  const timings = [
    { label: "HTTP (client-measured)", value: request.http_duration_ms },
    { label: "PostgREST transaction", value: request.server_timing_transaction_ms },
    { label: "Statement execute phase", value: request.statement_duration_ms },
  ].filter((t) => t.value != null);

  return (
    <Card data-testid="postgrest-request">
      <CardHeader className="pb-2">
        <CardTitle className="text-sm flex flex-wrap items-center gap-2">
          PostgREST request
          {request.session_context_missing ? (
            <Badge variant="outline">session context not logged</Badge>
          ) : (
            <code className="text-xs font-mono">{request.method} {request.endpoint}</code>
          )}
          {request.failure && <Badge variant="destructive">failed in production</Badge>}
        </CardTitle>
        <CardDescription>
          Imported from PostgreSQL log evidence. The endpoint's response is not replayed; comparisons are query-level.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4 text-sm">
        {request.failure && (
          <p className="text-destructive">
            The request statement failed: {request.failure}. PostgreSQL logs no duration for a failed statement.
          </p>
        )}
        <dl className="grid grid-cols-1 sm:grid-cols-2 gap-x-6 gap-y-2">
          {!request.session_context_missing && (
            <Field label="Database role" value={request.role} />
          )}
          <Field label="Replays run as" value={request.replay_role ? `${request.replay_role} (captured role; JWT claims are never replayed)` : "the connection's own role"} />
          <Field label="Replay search_path" value={request.replay_search_path ?? "the connection's search_path"} />
          {request.search_path && <Field label="Captured search_path" value={request.search_path} />}
          {settings.length > 0 && (
            <Field label="Request settings" value={settings.map(([k, v]) => `${k}=${v}`).join(", ")} />
          )}
          {request.query_id && <Field label="Query id" value={request.query_id} />}
        </dl>
        {timings.length > 0 && (
          <div className="grid grid-cols-1 sm:grid-cols-3 gap-3">
            {timings.map((t) => (
              <div key={t.label} className="rounded-md border border-border p-2">
                <p className="text-xs text-muted-foreground">{t.label}</p>
                <p className="font-mono">{formatFloat(t.value ?? 0)} ms</p>
              </div>
            ))}
          </div>
        )}
        {binds && binds.length > 0 && (
          <div>
            <p className="text-xs font-medium text-muted-foreground mb-1">Captured bind values (every plan and comparison uses these)</p>
            <ul className="font-mono text-xs space-y-0.5">
              {binds.map((b, i) => (
                <li key={i} className="break-all">${i + 1} = {b}</li>
              ))}
            </ul>
          </div>
        )}
        {request.plan_cache_note && (
          <p className="text-xs">
            <span className="font-medium">Plan cache: </span>
            {request.plan_cache_note}
          </p>
        )}
        {request.replay_limitations.length > 0 && (
          <div>
            <p className="text-xs font-medium text-muted-foreground mb-1">What the replay does not reproduce</p>
            <ul className="list-disc pl-5 space-y-1 text-xs text-muted-foreground">
              {request.replay_limitations.map((l, i) => (
                <li key={i}>{l}</li>
              ))}
            </ul>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

function Field({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className="font-mono text-xs break-all">{value}</dd>
    </div>
  );
}
