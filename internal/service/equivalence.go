package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/pgquerynarrative/pgquerynarrative/internal/queryrunner"
)

// Equivalence status values returned to API / reports.
//
// VerifiedEqual means every row of both results contributed to a full-result,
// order-independent fingerprint (count, then sum and xor of two independent 64-bit
// row hashes: 128 bits) and the fingerprints matched. That is strong whole-result
// verification, but it is probabilistic agreement of a fingerprint, not a literal
// proof, and its limits are worth naming:
//
//   - it compares row *text*, so two results with identical text but different
//     column types or column names fingerprint the same;
//   - it is deliberately order-independent, so ORDER BY differences are
//     invisible — where ordering is part of the query's contract, that must be
//     checked separately;
//   - a hash collision is astronomically unlikely but not impossible, and the hash
//     is not cryptographic, so it guards against a mistaken rewrite, not against
//     someone crafting data to collide.
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
//
// Every read either path issues — the fingerprint pair, or the COUNT(*) and
// sample pairs in the fallback — runs against one shared Runner.RunSnapshot
// transaction per path, never two separate Run calls. Two separate
// transactions can each observe a different concurrently committed write and
// agree or disagree by accident: e.g. comparing SELECT n and SELECT n-1
// against a row moving from n=1 to n=2 between two unsynchronized reads can
// make every single snapshot's true values (1 vs 0, or 2 vs 1 — never equal)
// look equal (1 vs 1) purely from the timing of an unrelated write. Holding
// one snapshot for every read in a path closes that gap.
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

	return compareByCountAndSample(ctx, runner, beforeSQL, afterSQL)
}

// compareByCountAndSample is compareResultEquivalence's fallback when
// compareByFingerprint cannot run (e.g. a result column with no text output,
// or — see parseCountResult/parseFingerprintResult's maxColumns interaction —
// a runner configured with a lower column cap than the fingerprint's 5
// aggregate columns need).
func compareByCountAndSample(ctx context.Context, runner *queryrunner.Runner, beforeSQL, afterSQL string) EquivalenceResult {
	out := EquivalenceResult{
		Status: EquivalenceUnverified,
		Notes:  "Result equivalence could not be verified (query error, timeout, or incomplete sample). Treat as unknown — not as a mismatch.",
	}

	beforeCountSQL, err := wrapCountSQL(beforeSQL)
	if err != nil {
		out.Notes = fmt.Sprintf("Result equivalence unverified: %v", err)
		return out
	}
	afterCountSQL, err := wrapCountSQL(afterSQL)
	if err != nil {
		out.Notes = fmt.Sprintf("Result equivalence unverified: %v", err)
		return out
	}
	// The sample fetch always orders by md5(row::text) before the LIMIT, even
	// though that ordering only matters once the full result is larger than
	// the cap (see wrapEquivalenceSampleSQL): this query's SQL text has to be
	// built now, before COUNT(*) has run, because all four reads below share
	// one snapshot — splitting COUNT(*) and the sample fetch into two
	// separate RunSnapshot calls would reopen exactly the concurrent-write
	// race this function exists to close, just with a shorter window (a
	// write landing between the count snapshot and a later, separate sample
	// snapshot could still make a stale count agree with a sample that no
	// longer matches it, or vice versa). Sorting a result that turns out to
	// already fit under the cap costs a sort on a small result set; that is
	// cheap, and correctness cannot wait for the count to decide whether it
	// is needed.
	beforeSampleSQL, err := wrapEquivalenceSampleSQL(beforeSQL, equivalenceSampleCap, true)
	if err != nil {
		out.Notes = fmt.Sprintf("Result equivalence unverified: sample SQL could not be built: %v", err)
		return out
	}
	afterSampleSQL, err := wrapEquivalenceSampleSQL(afterSQL, equivalenceSampleCap, true)
	if err != nil {
		out.Notes = fmt.Sprintf("Result equivalence unverified: sample SQL could not be built: %v", err)
		return out
	}

	results := runner.RunSnapshot(ctx, []queryrunner.SnapshotQuery{
		{SQL: beforeCountSQL, Limit: 1},
		{SQL: afterCountSQL, Limit: 1},
		{SQL: beforeSampleSQL, Limit: equivalenceSampleCap},
		{SQL: afterSampleSQL, Limit: equivalenceSampleCap},
	})
	beforeCount, errB := parseCountResult(results[0].Result, results[0].Err)
	afterCount, errA := parseCountResult(results[1].Result, results[1].Err)
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

	if results[2].Err != nil {
		out.Notes = fmt.Sprintf("COUNT(*) matched (%d rows) but sample run failed on original SQL: %v. Unverified — not a mismatch.", beforeCount, results[2].Err)
		return out
	}
	if results[3].Err != nil {
		out.Notes = fmt.Sprintf("COUNT(*) matched (%d rows) but sample run failed on candidate SQL: %v. Unverified — not a mismatch.", afterCount, results[3].Err)
		return out
	}
	before, after := results[2].Result, results[3].Result
	if before == nil || after == nil {
		out.Notes = fmt.Sprintf("COUNT(*) matched (%d rows) but sample result was empty/nil. Unverified — not a mismatch.", beforeCount)
		return out
	}

	// The sample request asked for up to equivalenceSampleCap rows (or all of
	// beforeCount, if fewer); the runner itself may still cap what one query
	// can return lower than that (maxRows, maxResultBytes, ...). If either side
	// came back shorter than expected, rows were silently dropped by a limit
	// unrelated to this comparison's own cap — not fully sampled, and not
	// known to be a full compare either, so neither VerifiedEqual nor
	// SampleMatch can be claimed from it, matching or not: a match over a
	// truncated, arbitrarily-ordered subset proves nothing about the rows
	// that were never fetched.
	expectedRows := beforeCount
	if expectedRows > int64(equivalenceSampleCap) {
		expectedRows = int64(equivalenceSampleCap)
	}
	if int64(before.RowCount) < expectedRows || int64(after.RowCount) < expectedRows {
		out.Notes = fmt.Sprintf(
			"COUNT(*) matched (%d rows) but the runner returned only %d (before) / %d (after) of the %d rows this comparison needed — a configured result limit (not this comparison's own %d-row cap) truncated the sample. Unverified — not a mismatch.",
			beforeCount, before.RowCount, after.RowCount, expectedRows, equivalenceSampleCap,
		)
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

// wrapEquivalenceSampleSQL renders every row of sql to PostgreSQL's own
// composite-to-text form inside the database — the same lossless rendering the
// primary hashtextextended fingerprint already trusts (see wrapFingerprintSQL)
// — instead of fetching structured column values for Go to decode. A row
// containing a JSON/JSONB value, such as PostgREST's whole-response wrapper,
// would otherwise have to pass through pgx's default JSON decode into a Go
// interface{}, which unmarshals every JSON number as a float64 and silently
// rounds an integer above 2^53 (9007199254740992) to the nearest representable
// value. Two candidate results that differ only in such a digit could then
// decode to the same float64, fingerprint identically, and be reported
// VerifiedEqual. Casting the whole row to text in SQL means no column value is
// ever decoded into a Go number at all, so this holds for any column shape, not
// only ones a caller happens to know might carry JSON.
func wrapEquivalenceSampleSQL(sql string, limit int, deterministic bool) (string, error) {
	inner, _, err := queryrunner.ExtractReadOnlySQL(sql)
	if err != nil {
		return "", err
	}
	inner = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(inner), ";"))
	if inner == "" {
		return "", fmt.Errorf("empty SQL")
	}
	order := ""
	if deterministic {
		order = " ORDER BY md5(pgqn_eq::text)"
	}
	return fmt.Sprintf(
		"SELECT pgqn_eq::text AS pgqn_row FROM (%s) AS pgqn_eq%s LIMIT %d",
		inner, order, limit,
	), nil
}

// parseCountResult extracts the COUNT(*) scalar from a wrapCountSQL result
// run via Runner.RunSnapshot.
func parseCountResult(res *queryrunner.Result, err error) (int64, error) {
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

// multisetFingerprint hashes a sorted list of per-row hashes so unordered
// SELECT results do not false-negative as Different. Each row must already be
// PostgreSQL's own composite-to-text rendering of the row — produced by
// wrapEquivalenceSampleSQL, never a Go-decoded column value — so that a
// JSON/JSONB value's exact digits survive instead of being rounded by a
// float64 decode. A row that is not that single text cell fails closed
// (ok=false) rather than silently hashing something else.
func multisetFingerprint(result *queryrunner.Result) (string, bool) {
	if result == nil {
		return "", false
	}
	rowHashes := make([]string, 0, len(result.Rows))
	for _, row := range result.Rows {
		text, ok := rowTextCell(row)
		if !ok {
			return "", false
		}
		sum := sha256.Sum256([]byte(text))
		rowHashes = append(rowHashes, hex.EncodeToString(sum[:]))
	}
	sort.Strings(rowHashes)
	blob, err := json.Marshal(rowHashes)
	if err != nil {
		return "", false
	}
	sum := sha256.Sum256(blob)
	return hex.EncodeToString(sum[:]), true
}

// rowTextCell returns row's single text cell (PostgreSQL's own row::text
// rendering), or ok=false if row is not shaped that way.
func rowTextCell(row []interface{}) (string, bool) {
	if len(row) != 1 {
		return "", false
	}
	switch t := row[0].(type) {
	case string:
		return t, true
	case []byte:
		return string(t), true
	default:
		return "", false
	}
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
	// Sum2 and Xor2 come from a second hash of the same row text with a different seed, so the two are
	// independent 64-bit values: 128 bits in all, not 64.
	Sum2 string
	Xor2 int64
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
       coalesce(bit_xor(hashtextextended(pgqn_eq::text, 0)), 0)::bigint AS pgqn_x,
       coalesce(sum(hashtextextended(pgqn_eq::text, 1)::numeric), 0)::text AS pgqn_s2,
       coalesce(bit_xor(hashtextextended(pgqn_eq::text, 1)), 0)::bigint AS pgqn_x2
FROM (%s) AS pgqn_eq`, inner), nil
}

// parseFingerprintResult extracts the five aggregate scalars from a
// wrapFingerprintSQL result run via Runner.RunSnapshot.
func parseFingerprintResult(res *queryrunner.Result, err error) (aggregateFingerprint, error) {
	var fp aggregateFingerprint
	if err != nil {
		return fp, err
	}
	if res == nil || len(res.Rows) == 0 || len(res.Rows[0]) < 5 {
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
	fp.Sum2 = fmt.Sprintf("%v", row[3])
	if fp.Xor2, err = asInt64(row[4]); err != nil {
		return fp, fmt.Errorf("fingerprint xor: %w", err)
	}
	return fp, nil
}

func (f aggregateFingerprint) equal(other aggregateFingerprint) bool {
	return f.Count == other.Count && f.Sum == other.Sum && f.Xor == other.Xor && f.Sum2 == other.Sum2 && f.Xor2 == other.Xor2
}

// compareByFingerprint compares two results with one aggregate pass each.
// Returns ok=false when either side cannot be fingerprinted — a result column
// whose type has no text output, for instance — so the caller can fall back to
// the count-plus-sample path rather than reporting a false Unverified.
func compareByFingerprint(ctx context.Context, runner *queryrunner.Runner, beforeSQL, afterSQL string) (EquivalenceResult, bool) {
	var out EquivalenceResult

	beforeWrapped, errB := wrapFingerprintSQL(beforeSQL)
	afterWrapped, errA := wrapFingerprintSQL(afterSQL)
	if errB != nil || errA != nil {
		return out, false
	}
	results := runner.RunSnapshot(ctx, []queryrunner.SnapshotQuery{
		{SQL: beforeWrapped, Limit: 1},
		{SQL: afterWrapped, Limit: 1},
	})
	beforeFP, errB := parseFingerprintResult(results[0].Result, results[0].Err)
	if errB != nil {
		return out, false
	}
	afterFP, errA := parseFingerprintResult(results[1].Result, results[1].Err)
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
