import { AlertTriangle, ArrowRight, ChevronDown, Wrench } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import type { PlanDiagnosis, PlanDiagnosisCause } from "@/api/client";

interface VerdictProps {
  diagnosis?: PlanDiagnosis;
  /** Projected impact of the recommended rewrite, when one has been computed. */
  projected?: { costPct?: number; partitionsBefore?: number; partitionsAfter?: number } | null;
  /** Invoked by the primary CTA — the page wires this to Suggest rewrite. */
  onFix?: () => void;
  fixLabel?: string;
  fixBusy?: boolean;
}

const severityStyle: Record<string, string> = {
  blocker: "border-destructive/40 bg-destructive/5",
  contributing: "border-amber-500/40 bg-amber-500/5",
};

const severityBadge: Record<string, "destructive" | "outline"> = {
  blocker: "destructive",
  contributing: "outline",
};

export function InvestigationVerdict({ diagnosis, projected, onFix, fixLabel, fixBusy }: VerdictProps) {
  if (!diagnosis || (!diagnosis.headline && !(diagnosis.causes && diagnosis.causes.length))) {
    return null;
  }

  const root = diagnosis.root_cause;
  const rest = (diagnosis.causes ?? []).filter((c) => c !== root && c.category !== root?.category);

  return (
    <section className="rounded-xl border border-border bg-card overflow-hidden">
      <div className="p-5 border-b border-border/60">
        <div className="flex items-start gap-3">
          <AlertTriangle className="h-5 w-5 text-destructive shrink-0 mt-0.5" />
          <div className="min-w-0 flex-1">
            <h2 className="text-lg font-semibold tracking-tight">{diagnosis.headline}</h2>
            {diagnosis.summary && (
              <p className="text-sm text-muted-foreground mt-1 leading-relaxed">{diagnosis.summary}</p>
            )}
          </div>
        </div>

        {root?.fix && (
          <div className="mt-4 flex flex-col sm:flex-row sm:items-center sm:justify-between gap-3 rounded-lg border border-primary/25 bg-primary/5 p-3">
            <div className="flex items-start gap-2 text-sm min-w-0">
              <Wrench className="h-4 w-4 text-primary shrink-0 mt-0.5" />
              <div className="min-w-0">
                <span className="font-medium">{root.fix}</span>
                {projected && (
                  <span className="text-muted-foreground">
                    {" "}
                    → projected{" "}
                    {typeof projected.costPct === "number" && <b>{projected.costPct > 0 ? "+" : ""}{projected.costPct}% cost</b>}
                    {typeof projected.partitionsBefore === "number" && typeof projected.partitionsAfter === "number" && (
                      <>
                        {typeof projected.costPct === "number" ? ", " : ""}
                        <b>{projected.partitionsBefore} → {projected.partitionsAfter} partitions</b>
                      </>
                    )}
                    {" "}
                    <span className="opacity-70">(dry EXPLAIN)</span>
                  </span>
                )}
              </div>
            </div>
            {onFix && (
              <Button size="sm" onClick={onFix} disabled={fixBusy} className="shrink-0">
                {fixLabel ?? "Preview the fix"}
                <ArrowRight className="h-4 w-4" />
              </Button>
            )}
          </div>
        )}
      </div>

      <div className="p-5 space-y-2">
        <p className="text-[11px] font-semibold uppercase tracking-wide text-muted-foreground">
          {diagnosis.causes && diagnosis.causes.length > 1 ? "Causes, ranked" : "Cause"}
        </p>
        {(diagnosis.causes ?? []).map((c, i) => (
          <CauseRow key={`${c.category}-${i}`} cause={c} isRoot={i === 0} />
        ))}

        {diagnosis.incidental && diagnosis.incidental.count > 0 && (
          <p className="text-xs text-muted-foreground pt-1">
            <span className="font-medium">Set aside:</span> {diagnosis.incidental.summary}
          </p>
        )}
      </div>

      {typeof diagnosis.raw_count === "number" && (
        <div className="px-5 py-2.5 border-t border-border/60 text-[11px] text-muted-foreground">
          Rolled up from {diagnosis.raw_count.toLocaleString()} raw plan findings
          {rest.length === 0 && diagnosis.causes && diagnosis.causes.length === 1 ? "." : ` into ${diagnosis.causes?.length ?? 0} cause(s).`}
        </div>
      )}
    </section>
  );
}

function CauseRow({ cause, isRoot }: { cause: PlanDiagnosisCause; isRoot: boolean }) {
  const sev = cause.severity in severityStyle ? cause.severity : "contributing";
  return (
    <div className={cn("rounded-lg border p-3", severityStyle[sev])}>
      <div className="flex flex-wrap items-center gap-2">
        <Badge variant={severityBadge[sev] ?? "outline"} className="text-[10px] capitalize shrink-0">
          {cause.severity}
        </Badge>
        <span className="font-medium text-sm">{cause.title}</span>
        {isRoot && typeof cause.cost_share === "number" && cause.cost_share >= 0.1 && (
          <span className="text-xs text-muted-foreground">~{Math.round(cause.cost_share * 100)}% of plan cost</span>
        )}
        {typeof cause.occurrences === "number" && cause.occurrences > 1 && (
          <span className="text-xs text-muted-foreground">×{cause.occurrences}</span>
        )}
      </div>
      {cause.detail && <p className="text-xs text-muted-foreground mt-1">{cause.detail}</p>}
      {cause.fix && !isRoot && (
        <p className="text-xs mt-1.5">
          <span className="text-muted-foreground">Fix: </span>
          {cause.fix}
        </p>
      )}
      {cause.evidence && cause.evidence.length > 0 && (
        <details className="mt-2 group">
          <summary className="text-[11px] text-muted-foreground cursor-pointer list-none flex items-center gap-1">
            <ChevronDown className="h-3 w-3 transition-transform group-open:rotate-0 -rotate-90" />
            Evidence ({cause.evidence.length})
          </summary>
          <ul className="mt-1.5 space-y-1 pl-4">
            {cause.evidence.map((e, i) => (
              <li key={i} className="text-[11px] text-muted-foreground font-mono leading-snug list-disc marker:text-muted-foreground/40">
                {e}
              </li>
            ))}
          </ul>
        </details>
      )}
    </div>
  );
}
