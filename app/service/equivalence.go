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
const (
	EquivalenceEqual      = "Equal"
	EquivalenceDifferent  = "Different"
	EquivalenceUnverified = "Unverified"
)

const equivalenceSampleCap = 1000

// EquivalenceResult is the tri-state outcome of comparing two query result sets.
// Status is never "Different" when a run failed — that is always Unverified.
type EquivalenceResult struct {
	Equal       *bool
	Status      string
	Notes       string
	BeforeCount int64
	AfterCount  int64
	SampleRows  int
	FullCompare bool // true when COUNT(*) <= sample cap and payloads compared fully
}

// compareResultEquivalence runs COUNT(*) on both queries, then compares a
// bounded multiset sample. Returns Unverified (Equal=nil) on any run error.
func compareResultEquivalence(ctx context.Context, runner *queryrunner.Runner, beforeSQL, afterSQL string) EquivalenceResult {
	out := EquivalenceResult{
		Status: EquivalenceUnverified,
		Notes:  "Result equivalence could not be verified (query error, timeout, or incomplete sample). Treat as unknown — not as a mismatch.",
	}
	if runner == nil {
		out.Notes = "Result equivalence unverified: no query runner."
		return out
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

	before, err := runner.Run(ctx, beforeSQL, equivalenceSampleCap)
	if err != nil {
		out.Notes = fmt.Sprintf("COUNT(*) matched (%d rows) but sample run failed on original SQL: %v. Unverified — not a mismatch.", beforeCount, err)
		return out
	}
	after, err := runner.Run(ctx, afterSQL, equivalenceSampleCap)
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
	eq := bh == ah
	out.Equal = &eq
	if !eq {
		out.Status = EquivalenceDifferent
		out.Notes = fmt.Sprintf(
			"COUNT(*) matched (%d) but sampled payloads differ (compared up to %d rows as an order-independent multiset). Do not deploy without review.",
			beforeCount, equivalenceSampleCap,
		)
		return out
	}

	out.Status = EquivalenceEqual
	if beforeCount <= int64(equivalenceSampleCap) {
		out.FullCompare = true
		out.Notes = fmt.Sprintf(
			"Full result compared: COUNT(*)=%d and all sampled rows match as an order-independent multiset.",
			beforeCount,
		)
	} else {
		out.Notes = fmt.Sprintf(
			"COUNT(*) matched (%d). Sampled %d rows match as an order-independent multiset — not a proof of full payload equality beyond the sample. Re-check on a representative parameter set before deploying.",
			beforeCount, out.SampleRows,
		)
	}
	return out
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
