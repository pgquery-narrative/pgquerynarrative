import { cn } from "@/lib/utils";
import type { PlanComparisonMetric, ComparePlansResult } from "@/api/client";
import { Badge } from "@/components/ui/badge";
import { Minus, Plus, TrendingDown, AlertTriangle } from "lucide-react";
import {
  equivalenceStatusOf,
  equivalenceLabel,
  equivalenceBlurb,
  isShippableEquivalence,
} from "@/lib/equivalence";

interface PlanCompareProps {
  comparison: ComparePlansResult;
  className?: string;
}

/** Before/after plan comparison view with blocking equivalence banner. */
export function PlanCompare({ comparison, className }: PlanCompareProps) {
  const { metrics, diff } = comparison;
  const status = equivalenceStatusOf(comparison);
  const shippable = isShippableEquivalence(status);
  const notes = comparison.result_equivalence_notes || equivalenceBlurb(status);

  return (
    <div className={cn("space-y-6", className)}>
      {!shippable && (
        <div
          role="alert"
          className={cn(
            "rounded-lg border p-4 text-sm space-y-1",
            status === "Different"
              ? "border-destructive/40 bg-destructive/10 text-destructive"
              : "border-amber-500/40 bg-amber-500/10 text-amber-900 dark:text-amber-100"
          )}
        >
          <p className="font-semibold flex items-center gap-2">
            <AlertTriangle className="h-4 w-4" />
            {status === "Different"
              ? "Result equivalence: Different — do not ship this candidate"
              : status === "NotRequested"
                ? "Result equivalence: not checked — enable result verification to compare rows"
                : "Result equivalence: Unverified — not shippable yet"}
          </p>
          <p className="text-xs opacity-90">{notes}</p>
          {(comparison.result_before_row_count != null || comparison.result_after_row_count != null) && (
            <p className="text-xs font-mono opacity-80">
              COUNT(*) before={comparison.result_before_row_count ?? "—"} after={comparison.result_after_row_count ?? "—"}
              {comparison.result_sample_rows != null && <> · sample={comparison.result_sample_rows}</>}
            </p>
          )}
        </div>
      )}

      <div className="overflow-x-auto rounded-lg border border-border/70">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b border-border/70 bg-muted/30">
              <th className="text-left p-3 font-medium">Evidence</th>
              <th className="text-left p-3 font-medium">Before</th>
              <th className="text-left p-3 font-medium">After</th>
              <th className="text-left p-3 font-medium">Change</th>
            </tr>
          </thead>
          <tbody>
            {metrics.map((row) => (
              <ComparisonRow key={row.evidence} row={row} />
            ))}
            <tr className="border-t border-border/50">
              <td className="p-3 font-medium">Result equivalence</td>
              <td className="p-3 font-mono text-xs text-muted-foreground">
                {comparison.result_before_row_count != null ? `COUNT(*)=${comparison.result_before_row_count}` : "—"}
              </td>
              <td className="p-3 font-mono text-xs text-muted-foreground">
                {comparison.result_after_row_count != null ? `COUNT(*)=${comparison.result_after_row_count}` : "—"}
              </td>
              <td className="p-3 space-y-1">
                {status === "VerifiedEqual" && <Badge variant="success">{equivalenceLabel(status)}</Badge>}
                {status === "SampleMatch" && <Badge variant="warning">{equivalenceLabel(status)}</Badge>}
                {status === "Different" && <Badge variant="destructive">{equivalenceLabel(status)}</Badge>}
                {(status === "Unverified" || status === "NotRequested") && (
                  <Badge variant="secondary">{equivalenceLabel(status)}</Badge>
                )}
                <p className="text-xs text-muted-foreground">{notes}</p>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      {diff && (
        <div className="grid gap-4 md:grid-cols-3">
          {diff.removed && diff.removed.length > 0 && (
            <DiffSection title="Removed" icon={Minus} items={diff.removed} variant="removed" />
          )}
          {diff.added && diff.added.length > 0 && (
            <DiffSection title="Added" icon={Plus} items={diff.added} variant="added" />
          )}
          {diff.improved && diff.improved.length > 0 && (
            <DiffSection title="Improved" icon={TrendingDown} items={diff.improved} variant="improved" />
          )}
        </div>
      )}
    </div>
  );
}

function ComparisonRow({ row }: { row: PlanComparisonMetric }) {
  const improved = row.change.startsWith("−") || row.change.includes("→");
  const muted = row.change === "estimate-only" || row.change === "n/a" || row.change === "Same";
  return (
    <tr className="border-b border-border/30 last:border-0">
      <td className="p-3 font-medium">{row.evidence}</td>
      <td className="p-3 font-mono text-muted-foreground">{row.before}</td>
      <td className="p-3 font-mono">{row.after}</td>
      <td className="p-3">
        <span className={cn("font-medium", improved && "text-success", muted && "text-muted-foreground")}>{row.change}</span>
      </td>
    </tr>
  );
}

function DiffSection({
  title,
  icon: Icon,
  items,
  variant,
}: {
  title: string;
  icon: React.ComponentType<{ className?: string }>;
  items: string[];
  variant: "removed" | "added" | "improved";
}) {
  const colors = {
    removed: "text-destructive",
    added: "text-success",
    improved: "text-primary",
  };
  const prefix = { removed: "−", added: "+", improved: "↓" };

  return (
    <div className="rounded-lg border border-border/70 p-4">
      <p className="text-sm font-semibold mb-2 flex items-center gap-2">
        <Icon className={cn("h-4 w-4", colors[variant])} />
        {title}
      </p>
      <ul className="space-y-1 text-sm font-mono">
        {items.map((item) => (
          <li key={item} className={colors[variant]}>
            {prefix[variant]} {item}
          </li>
        ))}
      </ul>
    </div>
  );
}
