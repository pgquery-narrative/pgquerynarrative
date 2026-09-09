package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/pgquerynarrative/pgquerynarrative/app/queryrunner"
)

// Equivalence status values returned to API / reports.
//
// VerifiedEqual means every row of both results contributed to a full-result,
// order-independent fingerprint (count + sum + xor of a 64-bit row hash) and the
// fingerprints matched. That is strong whole-result verification, but it is a
// fingerprint comparison, not a literal proof, and its limits are worth naming:
//
//   - it compares row *text*, so two results with identical text but different
//     column types or column names fingerprint the same;
//   - it is deliberately order-independent, so ORDER BY differences are
//     invisible — where ordering is part of the query's contract, that must be
//     checked separately;
//   - a hash collision is astronomically unlikely but not impossible.
//
// SampleMatch is the fallback when full-result fingerprinting could not run:
// supporting evidence over a bounded sample, never full verification.
const (
	EquivalenceVerifiedEqual = "VerifiedEqual"
	EquivalenceSampleMatch   = "SampleMatch"
	EquivalenceDifferent     = "Different"
	EquivalenceUnverified    = "Unverified"
	EquivalenceNotRequested  = "NotRequested"
)

const equivalenceSampleCap = 1000

// EquivalenceResult is the outcome of comparing two query result sets.
// Status is never "Different" when a run failed — that is always Unverified —
// and never "VerifiedEqual" unless every row of a COUNT(*) <= cap result matched.
type EquivalenceResult struct {
	Equal          *bool // true only for VerifiedEqual; false for Different; nil otherwise
	Status         string
	Notes          string
	CountsComputed bool // true once COUNT(*) ran on both sides — BeforeCount/AfterCount are real
	BeforeCount    int64
	AfterCount     int64
	SampleRows     int
	FullCompare    bool // true when COUNT(*) <= sample cap and every row was compared
}

// normalizeEquivalenceStatus maps a stored status string onto the current enum,
// tolerating the pre-PR {Equal, Different, Unverified} vocabulary and any
// unexpected value. An empty string stays empty (caller decides the default).
func normalizeEquivalenceStatus(s string) string {
	switch s {
	case EquivalenceVerifiedEqual, EquivalenceSampleMatch, EquivalenceDifferent,
		EquivalenceUnverified, EquivalenceNotRequested, "":
		return s
	case "Equal": // legacy: only ever set for a full compare
		return EquivalenceVerifiedEqual
	default:
		return EquivalenceUnverified
	}
}

// notRequestedEquivalence is the result when the caller did not ask to execute
// the queries (no verify_results, or the caller lacks the query permission).
func notRequestedEquivalence() EquivalenceResult {
	return EquivalenceResult{
		Status: EquivalenceNotRequested,
		Notes:  "Result equivalence was not checked — the compare only planned the queries. Re-run with result verification (requires the query permission) to compare rows.",
	}
}

// compareResultEquivalence runs COUNT(*) on both queries, then compares a
// deterministic bounded sample. The caller is responsible for authorizing query
// execution before calling this. Returns Unverified (Equal=nil) on any run error.
func compareResultEquivalence(ctx context.Context, runner *queryrunner.Runner, beforeSQL, afterSQL string) EquivalenceResult {
	out := EquivalenceResult{
		Status: EquivalenceUnverified,
		Notes:  "Result equivalence could not be verified (query error, timeout, or incomplete sample). Treat as unknown — not as a mismatch.",
	}
	if runner == nil {
		out.Notes = "Result equivalence unverified: no query runner."
		return out
	}

	// Primary path: one aggregate pass per side compares the *entire* result
	// with no sort and three scalars on the wire. Two executions instead of the
	// four the count-plus-sample path needed, and no 1000-row ceiling on what
	// can be called VerifiedEqual (full-result fingerprint match, not literal proof
	// — see the status constants for what the fingerprint does and does not cover).
	if res, ok := compareByFingerprint(ctx, runner, beforeSQL, afterSQL); ok {
		return res
	}

	beforeCount, errB := countQueryRows(ctx, runner, beforeSQL)
	afterCount, errA := countQueryRows(ctx, runner, afterSQL)
	if errB != nil || errA != nil {
		out.Notes = fmt.Sprintf(
			"Result equivalence unverified: COUNT(*) failed (before_err=%v after_err=%v). Unknown — not a mismatch.",
			errString(errB), errString(errA),
		)
		return out
	}
	out.CountsComputed = true
	out.BeforeCount = beforeCount
	out.AfterCount = afterCount
	if beforeCount != afterCount {
		eq := false
		out.Equal = &eq
		out.Status = EquivalenceDifferent
		out.Notes = fmt.Sprintf(
			"Full COUNT(*) differs: before=%d after=%d — do not deploy the candidate without review.",
			beforeCount, afterCount,
		)
		return out
	}

	// The full result already fits the cap: fetch it in any order and let the
	// order-independent multiset fingerprint compare it. Only when the result is
	// larger than the cap do we need the deterministic md5 ordering so the two
	// samples cover the same rows — and only then do we pay for the sort.
	deterministic := beforeCount > int64(equivalenceSampleCap)
	before, err := runEquivalenceSample(ctx, runner, beforeSQL, deterministic)
	if err != nil {
		out.Notes = fmt.Sprintf("COUNT(*) matched (%d rows) but sample run failed on original SQL: %v. Unverified — not a mismatch.", beforeCount, err)
		return out
	}
	after, err := runEquivalenceSample(ctx, runner, afterSQL, deterministic)
	if err != nil {
		out.Notes = fmt.Sprintf("COUNT(*) matched (%d rows) but sample run failed on candidate SQL: %v. Unverified — not a mismatch.", afterCount, err)
		return out
	}
	if before == nil || after == nil {
		out.Notes = fmt.Sprintf("COUNT(*) matched (%d rows) but sample result was empty/nil. Unverified — not a mismatch.", beforeCount)
		return out
	}

	bh, okB := multisetFingerprint(before)
	ah, okA := multisetFingerprint(after)
	if !okB || !okA {
		out.Notes = fmt.Sprintf("COUNT(*) matched (%d rows) but sample fingerprint failed. Unverified — not a mismatch.", beforeCount)
		return out
	}

	out.SampleRows = before.RowCount
	if after.RowCount < out.SampleRows {
		out.SampleRows = after.RowCount
	}
	if bh != ah {
		eq := false
		out.Equal = &eq
		out.Status = EquivalenceDifferent
		out.Notes = fmt.Sprintf(
			"COUNT(*) matched (%d) but the compared rows differ (deterministic sample of up to %d rows, order-independent multiset). Do not deploy without review.",
			beforeCount, equivalenceSampleCap,
		)
		return out
	}

	if beforeCount <= int64(equivalenceSampleCap) {
		eq := true
		out.Equal = &eq
		out.FullCompare = true
		out.Status = EquivalenceVerifiedEqual
		out.Notes = fmt.Sprintf(
			"Full result compared: COUNT(*)=%d and every row matched as an order-independent multiset.",
			beforeCount,
		)
		return out
	}

	// COUNT(*) matched but the result is larger than the sample cap: the two
	// deterministic md5-ordered samples matched, which is supporting evidence,
	// not full-result verification.
	out.Status = EquivalenceSampleMatch
	out.Notes = fmt.Sprintf(
		"COUNT(*) matched (%d). A deterministic %d-row sample (ordered by row hash) matched as an order-independent multiset — supporting evidence, not full-result verification. Re-check on a representative parameter set before deploying.",
		beforeCount, out.SampleRows,
	)
	return out
}

// runEquivalenceSample fetches up to equivalenceSampleCap rows of sql. When
// deterministic is set (the result is larger than the cap) it orders by
// md5(row::text) before the LIMIT so two queries with identical full result sets
// return the same subset; an un-ordered LIMIT would return an arbitrary — and
// possibly different — slice of each plan. When the whole result fits the cap
// the plain unordered fetch is enough (the fingerprint is order-independent) and
// avoids sorting the result set.
func runEquivalenceSample(ctx context.Context, runner *queryrunner.Runner, sql string, deterministic bool) (*queryrunner.Result, error) {
	if !deterministic {
		return runner.Run(ctx, sql, equivalenceSampleCap)
	}
	wrapped, err := wrapDeterministicSampleSQL(sql, equivalenceSampleCap)
	if err != nil {
		return nil, err
	}
	return runner.Run(ctx, wrapped, equivalenceSampleCap)
}

func wrapDeterministicSampleSQL(sql string, limit int) (string, error) {
	inner, _, err := queryrunner.ExtractReadOnlySQL(sql)
	if err != nil {
		return "", err
	}
	inner = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(inner), ";"))
	if inner == "" {
		return "", fmt.Errorf("empty SQL")
	}
	return fmt.Sprintf(
		"SELECT * FROM (%s) AS pgqn_eq ORDER BY md5(pgqn_eq::text) LIMIT %d",
		inner, limit,
	), nil
}

func countQueryRows(ctx context.Context, runner *queryrunner.Runner, sql string) (int64, error) {
	wrapped, err := wrapCountSQL(sql)
	if err != nil {
		return 0, err
	}
	res, err := runner.Run(ctx, wrapped, 1)
	if err != nil {
		return 0, err
	}
	if res == nil || len(res.Rows) == 0 || len(res.Rows[0]) == 0 {
		return 0, fmt.Errorf("empty COUNT(*) result")
	}
	return asInt64(res.Rows[0][0])
}

func wrapCountSQL(sql string) (string, error) {
	inner, _, err := queryrunner.ExtractReadOnlySQL(sql)
	if err != nil {
		return "", err
	}
	inner = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(inner), ";"))
	if inner == "" {
		return "", fmt.Errorf("empty SQL")
	}
	return "SELECT COUNT(*)::bigint AS pgqn_eq_n FROM (" + inner + ") AS pgqn_eq", nil
}

func asInt64(v interface{}) (int64, error) {
	switch n := v.(type) {
	case int64:
		return n, nil
	case int32:
		return int64(n), nil
	case int:
		return int64(n), nil
	case float64:
		return int64(n), nil
	case json.Number:
		return n.Int64()
	case string:
		var parsed int64
		_, err := fmt.Sscan(n, &parsed)
		return parsed, err
	default:
		return 0, fmt.Errorf("unsupported count type %T", v)
	}
}

func errString(err error) string {
	if err == nil {
		return "nil"
	}
	return err.Error()
}

// multisetFingerprint hashes column names plus a sorted list of per-row hashes so
// unordered SELECT results do not false-negative as Different.
func multisetFingerprint(result *queryrunner.Result) (string, bool) {
	if result == nil {
		return "", false
	}
	cols := make([]string, 0, len(result.Columns))
	for _, c := range result.Columns {
		cols = append(cols, c.Name)
	}
	rowHashes := make([]string, 0, len(result.Rows))
	for _, row := range result.Rows {
		payload, err := json.Marshal(row)
		if err != nil {
			return "", false
		}
		sum := sha256.Sum256(payload)
		rowHashes = append(rowHashes, hex.EncodeToString(sum[:]))
	}
	sort.Strings(rowHashes)
	blob, err := json.Marshal(struct {
		Columns []string `json:"columns"`
		Rows    []string `json:"rows"`
	}{Columns: cols, Rows: rowHashes})
	if err != nil {
		return "", false
	}
	sum := sha256.Sum256(blob)
	return hex.EncodeToString(sum[:]), true
}

// resultFingerprint kept for unit tests / stable single-result hashing.
func resultFingerprint(result *queryrunner.Result) (string, bool) {
	return multisetFingerprint(result)
}

// aggregateFingerprint is a single-pass, order-independent summary of a result set.
type aggregateFingerprint struct {
	Count int64
	Sum   string // numeric: the running sum can exceed int64
	Xor   int64
}

// wrapFingerprintSQL builds an aggregate that summarises the whole result in one
// pass, with no sort and no row transfer.
//
// This replaces "ORDER BY md5(row::text) LIMIT n", which had to compute a hash
// for every row and then top-N sort the entire result just to look at 1000 rows
// — work proportional to the result on a query the user already considers too
// slow, and it could only ever support SampleMatch above the cap.
//
// count, sum and bit_xor are all commutative, so row order cannot affect the
// answer, and together they separate multisets that any one of them would miss:
// count catches cardinality, sum catches value changes, and xor catches
// transpositions that happen to preserve the sum.
func wrapFingerprintSQL(sql string) (string, error) {
	inner, _, err := queryrunner.ExtractReadOnlySQL(sql)
	if err != nil {
		return "", err
	}
	inner = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(inner), ";"))
	if inner == "" {
		return "", fmt.Errorf("empty SQL")
	}
	return fmt.Sprintf(`SELECT count(*)::bigint AS pgqn_n,
       coalesce(sum(hashtextextended(pgqn_eq::text, 0)::numeric), 0)::text AS pgqn_s,
       coalesce(bit_xor(hashtextextended(pgqn_eq::text, 0)), 0)::bigint AS pgqn_x
FROM (%s) AS pgqn_eq`, inner), nil
}

func runResultFingerprint(ctx context.Context, runner *queryrunner.Runner, sql string) (aggregateFingerprint, error) {
	var fp aggregateFingerprint
	wrapped, err := wrapFingerprintSQL(sql)
	if err != nil {
		return fp, err
	}
	res, err := runner.Run(ctx, wrapped, 1)
	if err != nil {
		return fp, err
	}
	if res == nil || len(res.Rows) == 0 || len(res.Rows[0]) < 3 {
		return fp, fmt.Errorf("empty fingerprint result")
	}
	row := res.Rows[0]
	if fp.Count, err = asInt64(row[0]); err != nil {
		return fp, fmt.Errorf("fingerprint count: %w", err)
	}
	fp.Sum = fmt.Sprintf("%v", row[1])
	if fp.Xor, err = asInt64(row[2]); err != nil {
		return fp, fmt.Errorf("fingerprint xor: %w", err)
	}
	return fp, nil
}

func (f aggregateFingerprint) equal(other aggregateFingerprint) bool {
	return f.Count == other.Count && f.Sum == other.Sum && f.Xor == other.Xor
}

// compareByFingerprint compares two results with one aggregate pass each.
// Returns ok=false when either side cannot be fingerprinted — a result column
// whose type has no text output, for instance — so the caller can fall back to
// the count-plus-sample path rather than reporting a false Unverified.
func compareByFingerprint(ctx context.Context, runner *queryrunner.Runner, beforeSQL, afterSQL string) (EquivalenceResult, bool) {
	var out EquivalenceResult

	beforeFP, errB := runResultFingerprint(ctx, runner, beforeSQL)
	if errB != nil {
		return out, false
	}
	afterFP, errA := runResultFingerprint(ctx, runner, afterSQL)
	if errA != nil {
		return out, false
	}

	out.CountsComputed = true
	out.BeforeCount = beforeFP.Count
	out.AfterCount = afterFP.Count
	out.FullCompare = true

	if beforeFP.Count != afterFP.Count {
		eq := false
		out.Equal = &eq
		out.Status = EquivalenceDifferent
		out.Notes = fmt.Sprintf(
			"Row counts differ: before=%d after=%d — do not deploy the candidate without review.",
			beforeFP.Count, afterFP.Count,
		)
		return out, true
	}

	if !beforeFP.equal(afterFP) {
		eq := false
		out.Equal = &eq
		out.Status = EquivalenceDifferent
		out.Notes = fmt.Sprintf(
			"Both queries return %d rows, but the values differ (full-result order-independent checksum). Do not deploy without review.",
			beforeFP.Count,
		)
		return out, true
	}

	eq := true
	out.Equal = &eq
	out.Status = EquivalenceVerifiedEqual
	out.SampleRows = int(beforeFP.Count)
	out.Notes = fmt.Sprintf(
		"Full result compared: %d rows, matched by an order-independent checksum over every row (one aggregate pass per side, no sort).",
		beforeFP.Count,
	)
	return out, true
}
