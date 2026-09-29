import { describe, expect, it } from "vitest";
import { collapseFindings, collapsePartitionRepeats } from "./utils";
import { collapsePartitionSiblings, type PlanTreeNode } from "./plan-tree";

describe("collapseFindings", () => {
  it("groups monthly seq scans that only differ by partition and cost", () => {
    const findings = [
      { category: "seq_scan", message: "Sequential scan on demo.sales_2023_01 (estimated cost 12.50) — function-wrapped partition/index key blocks pruning" },
      { category: "seq_scan", message: "Sequential scan on demo.sales_2024_06 (estimated cost 880000.10) — function-wrapped partition/index key blocks pruning" },
      { category: "seq_scan", message: "Sequential scan on demo.regions (estimated cost 1.00) — likely acceptable for small or unfiltered scans" },
    ];
    const { items, rawCount } = collapseFindings(findings);
    expect(rawCount).toBe(3);
    expect(items).toHaveLength(2);
    expect(items[0].message).toContain("×2 similar partition scans");
    expect(items[0].message).not.toMatch(/estimated cost\s+[\d]/i);
  });

  it("groups partitions named without a schema prefix", () => {
    // planFindingMessage falls back to the bare relation when EXPLAIN reports no
    // schema, so these are real messages and must collapse like qualified ones.
    const { items } = collapseFindings([
      { category: "seq_scan", message: "Sequential scan on orders_2024_01 (estimated cost 10.00) — blocks pruning" },
      { category: "seq_scan", message: "Sequential scan on orders_2024_02 (estimated cost 99.00) — blocks pruning" },
    ]);
    expect(items).toHaveLength(1);
    expect(items[0].message).toContain("×2 similar partition scans");
  });

  it("reports the count for repeats that are not partition-shaped", () => {
    // Matches FormatCollapsedFinding in internal/queryrunner/finding_display.go so the
    // UI and the PDF do not disagree about how often a finding fired.
    const { items } = collapseFindings([
      { category: "sort", message: "High-cost Sort on demo.orders" },
      { category: "sort", message: "High-cost Sort on demo.orders" },
    ]);
    expect(items).toHaveLength(1);
    expect(items[0].message).toBe("High-cost Sort on demo.orders (×2 similar)");
  });
});

describe("collapsePartitionRepeats", () => {
  it("collapses sibling partition labels", () => {
    const got = collapsePartitionRepeats([
      "Seq Scan: sales_2023_01",
      "Seq Scan: sales_2023_02",
      "Seq Scan: sales_2023_03",
    ]);
    expect(got).toHaveLength(1);
    expect(got[0]).toContain("×3 partitions");
  });

  it("keeps a scan of the parent table out of the partition group", () => {
    // Stripping the suffix from "sales_2023_01" yields "sales", which is also a
    // real label — the two must not share a group.
    expect(
      collapsePartitionRepeats(["Seq Scan: sales", "Seq Scan: sales_2023_01", "Seq Scan: sales_2023_02"]),
    ).toEqual(["Seq Scan: sales", "Seq Scan: sales (×2 partitions)"]);
  });

  it("does not call plain duplicates partitions", () => {
    expect(collapsePartitionRepeats(["Sort", "Sort"])).toEqual(["Sort (×2 similar)"]);
    expect(collapsePartitionRepeats(["Seq Scan: users", "Seq Scan: users"])).toEqual([
      "Seq Scan: users (×2 similar)",
    ]);
  });

  it("leaves a lone partition label intact", () => {
    // Rewriting it to "Seq Scan: sales" would discard the month while collapsing
    // nothing.
    expect(collapsePartitionRepeats(["Seq Scan: sales_2023_01"])).toEqual(["Seq Scan: sales_2023_01"]);
  });
});

function leaf(relation: string, id: string): PlanTreeNode {
  return {
    id,
    nodeType: "Seq Scan",
    label: `Seq Scan: demo.${relation}`,
    schema: "demo",
    relation,
    estimatedRows: 10,
    cost: 5,
    severity: "attention",
    children: [],
  };
}

describe("collapsePartitionSiblings", () => {
  it("collapses three monthly partitions into one row", () => {
    const got = collapsePartitionSiblings([
      leaf("sales_2023_01", "0.0"),
      leaf("sales_2023_02", "0.1"),
      leaf("sales_2023_03", "0.2"),
    ]);
    expect(got).toHaveLength(1);
    expect(got[0].label).toContain("×3 partitions");
    expect(got[0].estimatedRows).toBe(30);
  });

  it("keeps a parent-table scan separate and sums the group's metrics", () => {
    const got = collapsePartitionSiblings([
      leaf("sales", "p"),
      leaf("sales_2023_01", "0.0"),
      leaf("sales_2023_02", "0.1"),
    ]);
    expect(got).toHaveLength(2);
    expect(got[0].label).toBe("Seq Scan: demo.sales");
    expect(got[1].label).toBe("Seq Scan: demo.sales (×2 partitions)");
    expect(got[1].estimatedRows).toBe(20);
  });

  it("takes the worst severity in the group", () => {
    const got = collapsePartitionSiblings([
      { ...leaf("s_2023_01", "a"), severity: "normal" },
      { ...leaf("s_2023_02", "b"), severity: "critical" },
    ]);
    expect(got[0].severity).toBe("critical");
  });

  it("leaves a lone partition, differing node types, and nodes with children alone", () => {
    expect(collapsePartitionSiblings([leaf("sales_2023_01", "0.0")])[0].label).toBe(
      "Seq Scan: demo.sales_2023_01",
    );
    expect(
      collapsePartitionSiblings([
        leaf("s_2023_01", "a"),
        { ...leaf("s_2023_02", "b"), nodeType: "Index Scan" },
      ]),
    ).toHaveLength(2);
    expect(
      collapsePartitionSiblings([
        { ...leaf("s_2023_01", "a"), children: [leaf("x", "c")] },
        { ...leaf("s_2023_02", "b"), children: [leaf("y", "d")] },
      ]),
    ).toHaveLength(2);
  });
});
