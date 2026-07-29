import { useState } from "react";
import { cn } from "@/lib/utils";
import { parsePlanTree, type PlanTreeNode } from "@/lib/plan-tree";
import { EvidencePanel } from "@/components/evidence-panel";
import { Badge } from "@/components/ui/badge";
import { ChevronRight, ChevronDown } from "lucide-react";
import { formatFloat } from "@/lib/utils";

interface PlanExplorerProps {
  plan: unknown;
  findings?: { node_type: string; message: string; confidence?: string; is_seq_scan: boolean }[];
  className?: string;
}

const severityDot = {
  normal: "bg-muted-foreground/40",
  attention: "bg-warning",
  high: "bg-orange-500",
  critical: "bg-destructive",
};

/** Interactive EXPLAIN plan tree explorer. */
export function PlanExplorer({ plan, findings, className }: PlanExplorerProps) {
  const root = parsePlanTree(plan);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set(["0"]));

  if (!root) {
    return (
      <p className="text-sm text-muted-foreground py-4">No plan data available. Run Explain to analyze the query plan.</p>
    );
  }

  const selectedNode = findNode(root, selectedId ?? root.id) ?? root;

  const toggle = (id: string) => {
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  return (
    <div className={cn("grid gap-4 lg:grid-cols-[1fr_320px]", className)}>
      <div className="rounded-lg border border-border/70 bg-card/50 p-3 font-mono text-xs overflow-x-auto">
        <PlanNodeRow
          node={root}
          depth={0}
          selectedId={selectedId ?? root.id}
          expanded={expanded}
          onSelect={setSelectedId}
          onToggle={toggle}
        />
      </div>
      <div className="rounded-lg border border-border/70 bg-card p-4">
        <div className="flex items-center justify-between mb-3">
          <p className="text-sm font-semibold">Evidence</p>
          <Badge variant={severityBadgeVariant(selectedNode.severity)} className="text-[10px] capitalize">
            {selectedNode.severity === "normal" ? "Normal" : selectedNode.severity.replace("_", " ")}
          </Badge>
        </div>
        <EvidencePanel node={selectedNode} />
      </div>
      {findings && findings.length > 0 && (
        <div className="lg:col-span-2 space-y-2">
          <p className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">Plan findings</p>
          {findings.map((f, i) => (
            <div key={i} className="flex items-start gap-2 text-sm p-2 rounded-md border border-border/50">
              <Badge variant={f.is_seq_scan ? "outline" : "secondary"} className="text-[10px] shrink-0">
                {f.node_type}
              </Badge>
              <span className="flex-1">{f.message}</span>
              {f.confidence && (
                <Badge variant="secondary" className="text-[10px] capitalize shrink-0">{f.confidence}</Badge>
              )}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function PlanNodeRow({
  node,
  depth,
  selectedId,
  expanded,
  onSelect,
  onToggle,
}: {
  node: PlanTreeNode;
  depth: number;
  selectedId: string;
  expanded: Set<string>;
  onSelect: (id: string) => void;
  onToggle: (id: string) => void;
}) {
  const hasChildren = node.children.length > 0;
  const isExpanded = expanded.has(node.id);
  const isSelected = selectedId === node.id;

  return (
    <div>
      <button
        type="button"
        onClick={() => onSelect(node.id)}
        className={cn(
          "flex items-center gap-1.5 w-full text-left py-1 px-1 rounded hover:bg-muted/50 transition-colors",
          isSelected && "bg-primary/10 ring-1 ring-primary/20"
        )}
        style={{ paddingLeft: `${depth * 16 + 4}px` }}
      >
        {hasChildren ? (
          <span
            role="button"
            tabIndex={0}
            onClick={(e) => { e.stopPropagation(); onToggle(node.id); }}
            onKeyDown={(e) => { if (e.key === "Enter") { e.stopPropagation(); onToggle(node.id); } }}
            className="shrink-0 p-0.5"
          >
            {isExpanded ? <ChevronDown className="h-3 w-3" /> : <ChevronRight className="h-3 w-3" />}
          </span>
        ) : (
          <span className="w-4 shrink-0" />
        )}
        <span className={cn("h-2 w-2 rounded-full shrink-0", severityDot[node.severity])} />
        <span className="truncate">{node.label}</span>
        {node.actualRows != null && (
          <span className="text-muted-foreground ml-auto shrink-0">
            {formatFloat(node.actualRows)} rows
          </span>
        )}
        {node.estimatedRows != null && node.actualRows == null && (
          <span className="text-muted-foreground ml-auto shrink-0">
            ~{formatFloat(node.estimatedRows)} est.
          </span>
        )}
      </button>
      {hasChildren && isExpanded && (
        <div>
          {node.children.map((child) => (
            <PlanNodeRow
              key={child.id}
              node={child}
              depth={depth + 1}
              selectedId={selectedId}
              expanded={expanded}
              onSelect={onSelect}
              onToggle={onToggle}
            />
          ))}
        </div>
      )}
    </div>
  );
}

function findNode(root: PlanTreeNode, id: string): PlanTreeNode | null {
  if (root.id === id) return root;
  for (const child of root.children) {
    const found = findNode(child, id);
    if (found) return found;
  }
  return null;
}

function severityBadgeVariant(severity: PlanTreeNode["severity"]) {
  if (severity === "critical") return "destructive" as const;
  if (severity === "high" || severity === "attention") return "outline" as const;
  return "secondary" as const;
}
