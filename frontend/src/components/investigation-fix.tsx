import { useState } from "react";
import { CheckCircle2, Circle, Loader2, XCircle } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";
import type { Investigation } from "@/api/client";

const STEPS = ["proposed", "verified", "applied", "confirmed"] as const;

interface FixProps {
  investigation: Investigation;
  busy: boolean;
  onUpdate: (body: { fix_status?: string; fix_reference?: string }) => void;
}

/** Fix lifecycle: proposed → verified → applied → confirmed/regressed.
 *  "applied" snapshots the linked query's latency; the poller re-measures it. */
export function InvestigationFix({ investigation, busy, onUpdate }: FixProps) {
  const status = investigation.fix_status ?? "proposed";
  const [ref, setRef] = useState(investigation.fix_reference ?? "");

  const stepIndex = STEPS.indexOf(status as (typeof STEPS)[number]);
  const terminal = status === "confirmed" || status === "regressed" || status === "abandoned";

  const delta =
    investigation.fix_baseline_mean_ms && investigation.fix_confirmed_mean_ms
      ? Math.round(
          ((investigation.fix_confirmed_mean_ms - investigation.fix_baseline_mean_ms) /
            investigation.fix_baseline_mean_ms) *
            100,
        )
      : undefined;

  return (
    <section className="rounded-xl border border-border bg-card p-5 space-y-4">
      <div className="flex items-center justify-between gap-3">
        <h3 className="text-sm font-semibold">Fix lifecycle</h3>
        <Badge
          variant={
            status === "confirmed" ? "success" : status === "regressed" ? "destructive" : "secondary"
          }
          className="capitalize text-[10px]"
        >
          {status}
        </Badge>
      </div>

      <ol className="flex flex-wrap items-center gap-1.5 text-xs">
        {STEPS.map((s, i) => {
          const done = stepIndex >= 0 && i <= stepIndex && !(status === "regressed" && s === "confirmed");
          return (
            <li key={s} className="flex items-center gap-1.5">
              {status === "regressed" && s === "confirmed" ? (
                <XCircle className="h-3.5 w-3.5 text-destructive" />
              ) : done ? (
                <CheckCircle2 className="h-3.5 w-3.5 text-primary" />
              ) : (
                <Circle className="h-3.5 w-3.5 text-muted-foreground/40" />
              )}
              <span className={cn("capitalize", done ? "text-foreground" : "text-muted-foreground")}>
                {status === "regressed" && s === "confirmed" ? "regressed" : s}
              </span>
              {i < STEPS.length - 1 && <span className="text-muted-foreground/30 mx-1">→</span>}
            </li>
          );
        })}
      </ol>

      {status === "applied" && (
        <p className="text-xs text-muted-foreground">
          {investigation.fix_confirmed_mean_ms != null && investigation.fix_baseline_mean_ms != null ? (
            <>
              Measuring in production: currently{" "}
              <b>{investigation.fix_confirmed_mean_ms.toFixed(1)} ms</b> vs{" "}
              <b>{investigation.fix_baseline_mean_ms.toFixed(1)} ms</b> at deploy. The poller confirms
              once it drops ~20%.
            </>
          ) : (
            <>Applied — the poller will re-measure this query's latency from pg_stat_statements.</>
          )}
        </p>
      )}

      {status === "confirmed" && delta != null && (
        <p className="text-xs text-success">
          Confirmed in production: mean latency{" "}
          <b>{investigation.fix_confirmed_mean_ms!.toFixed(1)} ms</b> (was{" "}
          {investigation.fix_baseline_mean_ms!.toFixed(1)} ms) — <b>{delta}%</b>.
        </p>
      )}

      {status === "regressed" && (
        <p className="text-xs text-destructive">
          No improvement measured after the grace period — the shipped change did not help this query.
        </p>
      )}

      {!terminal && (
        <div className="space-y-2">
          {(status === "proposed" || status === "verified") && (
            <Input
              value={ref}
              onChange={(e) => setRef(e.target.value)}
              placeholder="PR or ticket URL (optional)"
              className="text-xs"
            />
          )}
          <div className="flex flex-wrap gap-2">
            {status === "proposed" && (
              <Button size="sm" variant="outline" disabled={busy} onClick={() => onUpdate({ fix_status: "verified", fix_reference: ref || undefined })}>
                Mark verified
              </Button>
            )}
            {(status === "proposed" || status === "verified") && (
              <Button size="sm" disabled={busy} onClick={() => onUpdate({ fix_status: "applied", fix_reference: ref || undefined })}>
                {busy ? <Loader2 className="h-4 w-4 animate-spin" /> : null}
                Mark applied
              </Button>
            )}
            <Button size="sm" variant="ghost" disabled={busy} onClick={() => onUpdate({ fix_status: "abandoned" })}>
              Abandon
            </Button>
          </div>
        </div>
      )}

      {investigation.fix_reference && (
        <a href={investigation.fix_reference} target="_blank" rel="noreferrer" className="text-xs text-primary break-all">
          {investigation.fix_reference}
        </a>
      )}
    </section>
  );
}
