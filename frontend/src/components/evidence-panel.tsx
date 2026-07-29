import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";
import type { PlanTreeNode } from "@/lib/plan-tree";
import { nodeFinding } from "@/lib/plan-tree";
import { formatFloat } from "@/lib/utils";
import { Search, AlertTriangle } from "lucide-react";

const severityStyles = {
  normal: "border-border/60 bg-background",
  attention: "border-warning/40 bg-warning/5",
  high: "border-orange-500/40 bg-orange-500/5",
  critical: "border-destructive/50 bg-destructive/5",
};

const severityBadge = {
  normal: "secondary" as const,
  attention: "outline" as const,
  high: "outline" as const,
  critical: "destructive" as const,
};

interface EvidencePanelProps {
  node: PlanTreeNode;
  className?: string;
}

/** Evidence panel for a selected plan node. */
export function EvidencePanel({ node, className }: EvidencePanelProps) {
  const evidence = nodeFinding(node);

  return (
    <div className={cn("space-y-4 text-sm", className)}>
      <div>
        <p className="text-xs font-semibold uppercase tracking-wide text-muted-foreground mb-1">Finding</p>
        <p className="leading-relaxed">{evidence.finding}</p>
      </div>
      <div>
        <p className="text-xs font-semibold uppercase tracking-wide text-muted-foreground mb-1">Why it matters</p>
        <p className="text-muted-foreground leading-relaxed">{evidence.whyItMatters}</p>
      </div>
      <div>
        <p className="text-xs font-semibold uppercase tracking-wide text-muted-foreground mb-2 flex items-center gap-1.5">
          <Search className="h-3.5 w-3.5" /> Investigate
        </p>
        <ul className="space-y-1 text-muted-foreground">
          {evidence.investigate.map((item) => (
            <li key={item} className="flex items-start gap-2">
              <span className="text-primary mt-0.5">•</span>
              {item}
            </li>
          ))}
        </ul>
      </div>
      <div className="flex items-center gap-2 pt-1">
        <AlertTriangle className="h-4 w-4 text-muted-foreground" />
        <span className="text-xs text-muted-foreground">Confidence:</span>
        <Badge variant={evidence.confidence === "high" ? "default" : "secondary"} className="text-[10px] capitalize">
          {evidence.confidence}
        </Badge>
      </div>
      <div className="grid grid-cols-2 gap-2 pt-2 border-t border-border/50 text-xs">
        {node.estimatedRows != null && (
          <Metric label="Est. rows" value={formatFloat(node.estimatedRows)} />
        )}
        {node.actualRows != null && (
          <Metric label="Actual rows" value={formatFloat(node.actualRows)} />
        )}
        {node.cost != null && <Metric label="Cost" value={formatFloat(node.cost)} />}
        {node.actualTimeMs != null && (
          <Metric label="Actual time" value={`${formatFloat(node.actualTimeMs)}ms`} />
        )}
        {node.loops != null && <Metric label="Loops" value={String(node.loops)} />}
        {node.tempWrites != null && node.tempWrites > 0 && (
          <Metric label="Temp writes" value={String(node.tempWrites)} />
        )}
      </div>
    </div>
  );
}

function Metric({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <p className="text-muted-foreground">{label}</p>
      <p className="font-mono font-medium">{value}</p>
    </div>
  );
}
