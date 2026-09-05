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

/**
 * Map any stored status string onto the current enum, tolerating the pre-PR
 * `{Equal, Different, Unverified}` vocabulary and unexpected values.
 */
export function normalizeEquivalenceStatus(raw: string | null | undefined): EquivalenceStatus {
  if (raw && (KNOWN as string[]).includes(raw)) return raw as EquivalenceStatus;
  if (raw === "Equal") return "VerifiedEqual"; // legacy: only ever a full compare
  if (raw === "Different") return "Different";
  return "Unverified";
}

/** Resolve the equivalence status of a plan comparison, tolerating legacy rows. */
export function equivalenceStatusOf(comparison: {
  result_equivalence_status?: string | null;
  result_checksum_equal?: boolean | null;
}): EquivalenceStatus {
  const s = comparison.result_equivalence_status;
  if (s) return normalizeEquivalenceStatus(s);
  // No status string (a comparison stored before the field existed): the
  // checksum flag was only ever set true for a full-result compare.
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
    case "NotRequested":
      return "Not checked";
    case "Unverified":
    default:
      return "Unverified";
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
    default:
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
    case "NotRequested":
      return "Result equivalence was not checked — the compare only planned the queries. Re-run with result verification to compare rows.";
    case "Unverified":
    default:
      return "Equivalence could not be verified — treat as unknown, not a mismatch, and not shippable.";
  }
}
