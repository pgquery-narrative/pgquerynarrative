import { type ClassValue, clsx } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

export function formatFloat(v: number): string {
  return v.toLocaleString("en-US", { minimumFractionDigits: 2, maximumFractionDigits: 2 });
}

export function truncate(s: string, max: number): string {
  return s.length > max ? s.slice(0, max) + "..." : s;
}

/** Human-readable relative time (e.g. "22 minutes ago"). */
export function timeAgo(iso: string): string {
  const diff = Date.now() - new Date(iso).getTime();
  const mins = Math.floor(diff / 60_000);
  if (mins < 1) return "just now";
  if (mins < 60) return `${mins} minute${mins === 1 ? "" : "s"} ago`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return `${hours} hour${hours === 1 ? "" : "s"} ago`;
  const days = Math.floor(hours / 24);
  return days === 1 ? "Yesterday" : `${days} days ago`;
}

export interface PlanFindingLike {
  category?: string;
  message?: string;
}

const PARTITION_SUFFIX = /_\d{4}_\d{2}$/;
const ESTIMATED_COST = /\s*\(estimated cost [\d.]+\)\s*/gi;

// The schema prefix is optional: planFindingMessage falls back to the bare
// relation when EXPLAIN reports no schema (search_path resolved the table), so a
// pattern requiring "schema." would leave those messages un-normalized and every
// month would land in its own group — the collapse would silently never happen.
// Kept in step with findingPartitionRelationRe in internal/queryrunner/finding_display.go,
// which renders the same findings into reports and PDFs.
const PARTITION_RELATION = /\b(?:\w+\.)?\w+_\d{4}_\d{2}\b/g;

function partitionRefInMessage(message: string): boolean {
  return /\w+_\d{4}_\d{2}/.test(message);
}

function normalizePartitionFindingMessage(message: string): string {
  return message
    .replace(PARTITION_RELATION, "{partition}")
    .replace(ESTIMATED_COST, " ")
    .replace(/\s+/g, " ")
    .trim();
}

/**
 * Groups repeated partition-level findings that differ only by child partition name and cost.
 */
export function collapseFindings(findings: PlanFindingLike[]): {
  items: PlanFindingLike[];
  rawCount: number;
} {
  const rawCount = findings.length;
  const groups = new Map<string, PlanFindingLike[]>();
  const order: string[] = [];

  for (const f of findings) {
    const message = f.message ?? "";
    const category = f.category ?? "";
    const key = partitionRefInMessage(message)
      ? `${category}::${normalizePartitionFindingMessage(message)}`
      : `${category}::${message}`;
    if (!groups.has(key)) {
      groups.set(key, [f]);
      order.push(key);
    } else {
      groups.get(key)!.push(f);
    }
  }

  const items = order.map((key) => {
    const group = groups.get(key)!;
    if (group.length === 1) {
      return group[0];
    }
    const first = group[0];
    const message = first.message ?? "";
    if (!partitionRefInMessage(message)) {
      // A repeat that is not partition-shaped still repeated. Report the count
      // rather than dropping it, matching FormatCollapsedFinding in
      // internal/queryrunner/finding_display.go so the UI and the PDF agree.
      return { category: first.category, message: `${message.trim()} (×${group.length} similar)` };
    }
    const norm = normalizePartitionFindingMessage(message);
    const tail = norm.includes("—") ? norm.split("—").slice(1).join("—").trim() : norm;
    return {
      category: first.category,
      message: `×${group.length} similar partition scans — ${tail}`,
    };
  });

  return { items, rawCount };
}

/** Collapses labels like "Seq Scan: sales_2023_01" across sibling partitions. */
export function collapsePartitionRepeats(labels: string[]): string[] {
  // isPartition is carried per group so the rendered suffix tells the truth: a
  // group of real sibling partitions reads "(×N partitions)", while plain
  // duplicates of the same node read "(×N similar)".
  // original is kept alongside template so a group of one renders unchanged:
  // rewriting a lone "Seq Scan: sales_2023_01" to "Seq Scan: sales" would throw
  // away the month while collapsing nothing.
  const groups = new Map<
    string,
    { template: string; original: string; count: number; isPartition: boolean }
  >();
  const order: string[] = [];

  const add = (key: string, template: string, original: string, isPartition: boolean) => {
    const existing = groups.get(key);
    if (existing) {
      existing.count++;
      return;
    }
    groups.set(key, { template, original, count: 1, isPartition });
    order.push(key);
  };

  for (const label of labels) {
    const m = label.match(/^(.+?): (.+)$/);
    if (!m) {
      add(`L|${label}`, label, label, false);
      continue;
    }
    const nodeType = m[1];
    const relation = m[2];
    const isPartition = PARTITION_SUFFIX.test(relation);
    if (!isPartition) {
      add(`L|${label}`, label, label, false);
      continue;
    }
    // Namespaced apart from literal labels: stripping the suffix from
    // "Seq Scan: sales_2023_01" yields "Seq Scan: sales", which is also a real
    // label for a scan of the parent relation. Sharing one key would fold a
    // parent-table scan into the partition group and render it as one of the
    // children.
    const base = relation.replace(PARTITION_SUFFIX, "");
    add(`P|${nodeType}: ${base}`, `${nodeType}: ${base}`, label, true);
  }

  return order.map((key) => {
    const g = groups.get(key)!;
    if (g.count <= 1) {
      return g.original;
    }
    return g.isPartition
      ? `${g.template} (×${g.count} partitions)`
      : `${g.template} (×${g.count} similar)`;
  });
}
