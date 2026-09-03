import type { ComparePlansResult } from "@/api/client";

/**
 * How far result equivalence between an original query and a candidate rewrite
 * was checked. Only `VerifiedEqual` is a full-result proof.
 */
export type EquivalenceStatus =
  | "VerifiedEqual"
  | "SampleMatch"
  | "Different"
  | "Unverified"
  | "NotRequested";

const KNOWN: EquivalenceStatus[] = [
  "VerifiedEqual",
  "SampleMatch",
  "Different",
  "Unverified",
  "NotRequested",
];

/** Resolve the equivalence status of a plan comparison, tolerating legacy rows. */
export function equivalenceStatusOf(comparison: {
  result_equivalence_status?: string | null;
  result_checksum_equal?: boolean | null;
}): EquivalenceStatus {
  const s = comparison.result_equivalence_status;
  if (s && (KNOWN as string[]).includes(s)) return s as EquivalenceStatus;
  // Legacy comparison with no status string: the checksum flag was only ever
  // set true for a full-result compare.
  if (comparison.result_checksum_equal === true) return "VerifiedEqual";
  if (comparison.result_checksum_equal === false) return "Different";
  return "Unverified";
}

/** A report/candidate is shippable only when equivalence was actually confirmed. */
export function isShippableEquivalence(status: EquivalenceStatus): boolean {
  return status === "VerifiedEqual" || status === "SampleMatch";
}

export function equivalenceLabel(status: EquivalenceStatus): string {
  switch (status) {
    case "VerifiedEqual":
      return "Verified equal";
    case "SampleMatch":
      return "Sample match";
    case "Different":
      return "Different";
    case "Unverified":
      return "Unverified";
    case "NotRequested":
      return "Not checked";
  }
}

export type EquivalenceTone = "success" | "warning" | "destructive" | "muted";

export function equivalenceTone(status: EquivalenceStatus): EquivalenceTone {
  switch (status) {
    case "VerifiedEqual":
      return "success";
    case "SampleMatch":
      return "warning";
    case "Different":
      return "destructive";
    case "Unverified":
    case "NotRequested":
      return "muted";
  }
}

/** Default caption when the backend supplied no equivalence notes. */
export function equivalenceBlurb(status: EquivalenceStatus): string {
  switch (status) {
    case "VerifiedEqual":
      return "Full result compared — every row matched between original and candidate.";
    case "SampleMatch":
      return "COUNT(*) matched and a deterministic sample matched — supporting evidence, not a full-result proof. Re-check on a representative parameter set before deploying.";
    case "Different":
      return "Results differ — do not deploy this candidate.";
    case "Unverified":
      return "Equivalence could not be verified — treat as unknown, not a mismatch, and not shippable.";
    case "NotRequested":
      return "Result equivalence was not checked — the compare only planned the queries. Re-run with result verification to compare rows.";
  }
}

export function equivalenceComparison(comparison: ComparePlansResult): EquivalenceStatus {
  return equivalenceStatusOf(comparison);
}
