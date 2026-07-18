package service

import (
	"testing"
	"time"
)

// TestWebhookDeliveryIdempotencyKey_StableAcrossReportRegeneration verifies the delivery ID
// is derived only from schedule_run_id, never report_id. This is the core invariant that
// fixes the "regenerate report + resend with a new ID" bug: crash recovery can reuse a
// different report_id for the same schedule_run_id (see storeReport's unique-violation
// fallback) and the webhook delivery ID must still stay identical so the receiver's
// X-PGQN-Delivery-ID dedup keeps working and retries do not appear as new deliveries.
func TestWebhookDeliveryIdempotencyKey_StableAcrossReportRegeneration(t *testing.T) {
	runID := "11111111-1111-1111-1111-111111111111"

	keyWithReportA, err := webhookDeliveryIdempotencyKey(runID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Simulate a regenerated report for the same run (different report_id) — the key
	// function doesn't even take report_id as input, so it is structurally impossible for
	// report regeneration to change the delivery ID.
	keyWithReportB, err := webhookDeliveryIdempotencyKey(runID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if keyWithReportA != keyWithReportB {
		t.Fatalf("delivery id changed across calls for the same run: %q vs %q", keyWithReportA, keyWithReportB)
	}
}

func TestWebhookDeliveryIdempotencyKey_DiffersAcrossRuns(t *testing.T) {
	keyA, err := webhookDeliveryIdempotencyKey("11111111-1111-1111-1111-111111111111")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	keyB, err := webhookDeliveryIdempotencyKey("22222222-2222-2222-2222-222222222222")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if keyA == keyB {
		t.Fatalf("distinct schedule runs produced the same delivery id: %q", keyA)
	}
}

func TestWebhookDeliveryIdempotencyKey_RequiresScheduleRunID(t *testing.T) {
	for _, in := range []string{"", "   "} {
		if _, err := webhookDeliveryIdempotencyKey(in); err == nil {
			t.Fatalf("expected error for schedule_run_id %q; a silent fallback here would resurrect the unstable-ID bug", in)
		}
	}
}

func TestClassifyDeliveryHTTPStatus(t *testing.T) {
	tests := []struct {
		name           string
		code           string // documents intent; parsed below
		codeInt        int
		wantStatus     string
		wantClass      string
		wantHTTPStatus int
	}{
		{name: "200 delivered", codeInt: 200, wantStatus: "delivered", wantClass: "2xx", wantHTTPStatus: 200},
		{name: "204 delivered", codeInt: 204, wantStatus: "delivered", wantClass: "2xx", wantHTTPStatus: 204},
		{name: "299 delivered edge", codeInt: 299, wantStatus: "delivered", wantClass: "2xx", wantHTTPStatus: 299},
		{name: "408 retryable", codeInt: 408, wantStatus: "failed", wantClass: "retryable_4xx", wantHTTPStatus: 408},
		{name: "429 retryable", codeInt: 429, wantStatus: "failed", wantClass: "retryable_4xx", wantHTTPStatus: 429},
		{name: "400 dead letter", codeInt: 400, wantStatus: "dead_letter", wantClass: "4xx", wantHTTPStatus: 400},
		{name: "404 dead letter", codeInt: 404, wantStatus: "dead_letter", wantClass: "4xx", wantHTTPStatus: 404},
		{name: "499 dead letter", codeInt: 499, wantStatus: "dead_letter", wantClass: "4xx", wantHTTPStatus: 499},
		{name: "500 retryable", codeInt: 500, wantStatus: "failed", wantClass: "5xx", wantHTTPStatus: 500},
		{name: "503 retryable", codeInt: 503, wantStatus: "failed", wantClass: "5xx", wantHTTPStatus: 503},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, httpStatus, respBytes, errMsg, class := classifyDeliveryHTTPStatus(tt.codeInt, 123)
			if status != tt.wantStatus {
				t.Errorf("status = %q, want %q", status, tt.wantStatus)
			}
			if class != tt.wantClass {
				t.Errorf("response_class = %q, want %q", class, tt.wantClass)
			}
			if httpStatus != tt.wantHTTPStatus {
				t.Errorf("httpStatus = %d, want %d", httpStatus, tt.wantHTTPStatus)
			}
			if respBytes != 123 {
				t.Errorf("respBytes = %d, want 123 (passthrough)", respBytes)
			}
			if tt.wantStatus == "delivered" && errMsg != "" {
				t.Errorf("errMsg = %q, want empty on success", errMsg)
			}
			if tt.wantStatus != "delivered" && errMsg == "" {
				t.Errorf("errMsg = empty, want non-empty on failure/dead_letter")
			}
		})
	}
}

func TestClassifyOutboxResult(t *testing.T) {
	t.Run("delivered stays delivered regardless of attempt count", func(t *testing.T) {
		final, _ := classifyOutboxResult("delivered", 0)
		if final != "delivered" {
			t.Fatalf("final = %q, want delivered", final)
		}
	})

	t.Run("dead_letter from HTTP classification stays dead_letter", func(t *testing.T) {
		final, _ := classifyOutboxResult("dead_letter", 0)
		if final != "dead_letter" {
			t.Fatalf("final = %q, want dead_letter", final)
		}
	})

	t.Run("failed retries with backoff before exhausting attempts", func(t *testing.T) {
		before := time.Now().UTC()
		final, next := classifyOutboxResult("failed", 0)
		if final != "pending" {
			t.Fatalf("final = %q, want pending (retryable)", final)
		}
		if !next.After(before) {
			t.Fatalf("next_attempt_at = %v, want a future time (backoff applied)", next)
		}
	})

	t.Run("failed dead-letters once attempts are exhausted", func(t *testing.T) {
		final, _ := classifyOutboxResult("failed", maxWebhookAttempts)
		if final != "dead_letter" {
			t.Fatalf("final = %q, want dead_letter at max attempts", final)
		}
	})

	t.Run("failed dead-letters beyond max attempts too", func(t *testing.T) {
		final, _ := classifyOutboxResult("failed", maxWebhookAttempts+3)
		if final != "dead_letter" {
			t.Fatalf("final = %q, want dead_letter beyond max attempts", final)
		}
	})
}

func TestWebhookBackoff_MonotonicAndCapped(t *testing.T) {
	prev := time.Duration(0)
	for attempt := 0; attempt < 10; attempt++ {
		d := webhookBackoff(attempt)
		if d < prev {
			t.Fatalf("backoff decreased at attempt %d: %v < %v", attempt, d, prev)
		}
		if d > webhookRetryMaxBackoff {
			t.Fatalf("backoff exceeded cap at attempt %d: %v > %v", attempt, d, webhookRetryMaxBackoff)
		}
		prev = d
	}
	if got := webhookBackoff(-1); got != webhookRetryBaseBackoff {
		t.Fatalf("webhookBackoff(-1) = %v, want base backoff for negative input", got)
	}
}
