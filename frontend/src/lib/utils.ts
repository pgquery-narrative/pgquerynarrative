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

function partitionRefInMessage(message: string): boolean {
  return /\w+_\d{4}_\d{2}/.test(message);
}

function normalizePartitionFindingMessage(message: string): string {
  return message
    .replace(/\b\w+\.\w+_\d{4}_\d{2}\b/g, "{partition}")
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
      return first;
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
  const groups = new Map<string, { template: string; count: number }>();
  const order: string[] = [];

  for (const label of labels) {
    const m = label.match(/^(.+?): (.+)$/);
    if (!m) {
      if (!groups.has(label)) {
        groups.set(label, { template: label, count: 1 });
        order.push(label);
      } else {
        groups.get(label)!.count++;
      }
      continue;
    }
    const nodeType = m[1];
    const relation = m[2];
    const base = PARTITION_SUFFIX.test(relation) ? relation.replace(PARTITION_SUFFIX, "") : relation;
    const key = PARTITION_SUFFIX.test(relation) ? `${nodeType}: ${base}` : label;
    if (!groups.has(key)) {
      groups.set(key, {
        template: PARTITION_SUFFIX.test(relation) ? `${nodeType}: ${base}` : label,
        count: 1,
      });
      order.push(key);
    } else {
      groups.get(key)!.count++;
    }
  }

  return order.map((key) => {
    const g = groups.get(key)!;
    if (g.count > 1) {
      return `${g.template} (×${g.count} partitions)`;
    }
    return g.template;
  });
}
