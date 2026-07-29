/** Severity levels for plan node visualization. */
export type PlanSeverity = "normal" | "attention" | "high" | "critical";

/** One node in the EXPLAIN plan tree. */
export interface PlanTreeNode {
  id: string;
  nodeType: string;
  label: string;
  schema?: string;
  relation?: string;
  estimatedRows?: number;
  actualRows?: number;
  estimateError?: number;
  cost?: number;
  actualTimeMs?: number;
  loops?: number;
  buffers?: string;
  tempReads?: number;
  tempWrites?: number;
  rowsRemovedByFilter?: number;
  filter?: string;
  severity: PlanSeverity;
  children: PlanTreeNode[];
}

/** Parse PostgreSQL EXPLAIN (FORMAT JSON) into a plan tree. */
export function parsePlanTree(plan: unknown): PlanTreeNode | null {
  if (!plan || typeof plan !== "object") return null;
  const roots = plan as Array<{ Plan?: Record<string, unknown> }>;
  if (!Array.isArray(roots) || roots.length === 0 || !roots[0]?.Plan) return null;
  return walkNode(roots[0].Plan, "0");
}

function walkNode(node: Record<string, unknown>, id: string): PlanTreeNode {
  const nodeType = String(node["Node Type"] ?? "Unknown");
  const schema = node["Schema"] as string | undefined;
  const relation = node["Relation Name"] as string | undefined;
  const estimatedRows = num(node["Plan Rows"]);
  const actualRows = num(node["Actual Rows"]);
  const cost = num(node["Total Cost"]);
  const actualTimeMs = num(node["Actual Total Time"]);
  const loops = num(node["Actual Loops"]) ?? num(node["Plan Loops"]);
  const tempReads = num(node["Temp Read Blocks"]);
  const tempWrites = num(node["Temp Written Blocks"]);
  const rowsRemoved = num(node["Rows Removed by Filter"]);
  const filter = node["Filter"] as string | undefined;

  let estimateError: number | undefined;
  if (estimatedRows && actualRows && estimatedRows > 0) {
    estimateError = actualRows / estimatedRows;
  }

  const severity = computeSeverity(nodeType, estimateError, rowsRemoved, tempWrites);

  let label = nodeType;
  if (relation) {
    label = schema ? `${nodeType}: ${schema}.${relation}` : `${nodeType}: ${relation}`;
  }

  const bufferParts: string[] = [];
  const sharedHit = num(node["Shared Hit Blocks"]);
  const sharedRead = num(node["Shared Read Blocks"]);
  if (sharedHit != null || sharedRead != null) {
    bufferParts.push(`hit=${sharedHit ?? 0} read=${sharedRead ?? 0}`);
  }

  const children: PlanTreeNode[] = [];
  const childPlans = node["Plans"] as unknown[];
  if (Array.isArray(childPlans)) {
    childPlans.forEach((child, i) => {
      if (child && typeof child === "object") {
        children.push(walkNode(child as Record<string, unknown>, `${id}.${i}`));
      }
    });
  }

  return {
    id,
    nodeType,
    label,
    schema,
    relation,
    estimatedRows,
    actualRows,
    estimateError,
    cost,
    actualTimeMs,
    loops,
    buffers: bufferParts.length > 0 ? bufferParts.join(", ") : undefined,
    tempReads,
    tempWrites,
    rowsRemovedByFilter: rowsRemoved,
    filter,
    severity,
    children,
  };
}

function num(v: unknown): number | undefined {
  if (typeof v === "number" && !Number.isNaN(v)) return v;
  return undefined;
}

function computeSeverity(
  nodeType: string,
  estimateError?: number,
  rowsRemoved?: number,
  tempWrites?: number,
): PlanSeverity {
  if (tempWrites && tempWrites > 1000) return "critical";
  if (estimateError && (estimateError >= 100 || estimateError <= 0.01)) return "critical";
  if (nodeType === "Seq Scan" && rowsRemoved && rowsRemoved > 1_000_000) return "high";
  if (estimateError && (estimateError >= 10 || estimateError <= 0.1)) return "high";
  if (nodeType === "Seq Scan") return "attention";
  if (tempWrites && tempWrites > 0) return "attention";
  return "normal";
}

/** Build an evidence finding message for a plan node. */
export function nodeFinding(node: PlanTreeNode): {
  finding: string;
  whyItMatters: string;
  investigate: string[];
  confidence: "high" | "medium" | "low";
} {
  const items: string[] = [];
  let finding = `Plan node: ${node.label}`;
  let why = "This node contributes to overall query cost.";
  let confidence: "high" | "medium" | "low" = "low";

  if (node.estimateError && node.estimatedRows != null && node.actualRows != null) {
    const ratio = node.estimateError;
    finding = `The planner estimated ${formatNum(node.estimatedRows)} rows, but this node returned ${formatNum(node.actualRows)} rows—a ${ratio >= 1 ? `${ratio.toFixed(0)}×` : `${(1 / ratio).toFixed(0)}×`} ${ratio >= 1 ? "underestimation" : "overestimation"}.`;
    why = "The misestimated row count may have affected join strategy and caused significantly more work downstream.";
    confidence = ratio >= 100 || ratio <= 0.01 ? "high" : "medium";
    items.push("Column statistics", "Correlated predicates", "Extended statistics", "Stale ANALYZE data");
  } else if (node.nodeType === "Seq Scan" && node.rowsRemovedByFilter && node.rowsRemovedByFilter > 10000) {
    finding = `Sequential scan on ${node.relation ?? "relation"} removes ${formatNum(node.rowsRemovedByFilter)} rows by filter.`;
    why = "A large fraction of rows are read and discarded, which may indicate a missing or unusable index.";
    confidence = "medium";
    items.push("Index definitions on filter columns", "Predicate shape (functions on indexed columns)", "Table statistics freshness");
  } else if (node.tempWrites && node.tempWrites > 0) {
    finding = `This node wrote ${node.tempWrites} temporary blocks to disk.`;
    why = "Spilling to disk increases latency and I/O pressure.";
    confidence = "high";
    items.push("work_mem setting", "Sort/hash input size", "Row width and aggregation");
  }

  if (items.length === 0) {
    items.push("Predicate selectivity", "Join order", "Parallelism settings");
  }

  return { finding, whyItMatters: why, investigate: items, confidence };
}

function formatNum(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1000) return `${(n / 1000).toFixed(1)}k`;
  return String(Math.round(n));
}

/** Collect all nodes from a plan tree (flat list). */
export function flattenPlanTree(root: PlanTreeNode | null): PlanTreeNode[] {
  if (!root) return [];
  const out: PlanTreeNode[] = [root];
  for (const child of root.children) {
    out.push(...flattenPlanTree(child));
  }
  return out;
}
