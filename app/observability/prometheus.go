package observability

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PoolMetric identifies a connection pool for metrics rendering. It mirrors db.NamedPool
// but is defined locally (rather than imported from app/db) to avoid an import cycle:
// app/db depends on app/config, which depends on app/audit, which depends on this package.
type PoolMetric struct {
	Name string
	Role string
	Pool *pgxpool.Pool
}

var (
	requestTotal        atomic.Int64
	requestErrors       atomic.Int64
	queryRuns           atomic.Int64
	queryTimeouts       atomic.Int64
	llmCalls            atomic.Int64
	schedulerRuns       atomic.Int64
	schedulerFailures   atomic.Int64
	scheduleDead        atomic.Int64
	leaseRecoveries     atomic.Int64
	webhookDeliveries   atomic.Int64
	webhookFailures     atomic.Int64
	webhookDead         atomic.Int64
	authFailures        atomic.Int64
	authzDenials        atomic.Int64
	llmBudgetDenials    atomic.Int64
	llmTokensTotal      atomic.Int64
	auditWriteFails     atomic.Int64
	webhookRejected     atomic.Int64
	rateLimitStorageF   atomic.Int64
	llmBudgetFailClosed atomic.Int64
	llmBudgetExpired    atomic.Int64
	embedCalls          atomic.Int64
	embedDenials        atomic.Int64
	embedErrors         atomic.Int64
	embedLatencySumMS   atomic.Int64
	embedLatencyCount   atomic.Int64

	// Fixed latency buckets in milliseconds for HTTP request duration histogram.
	latencyBucketsMS = []int64{5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000}
	latencyCounts    [12]atomic.Int64 // len(buckets)+1 (+Inf)
	latencySumMS     atomic.Int64
)

// IncRequest increments total HTTP requests observed by middleware.
func IncRequest() { requestTotal.Add(1) }

// ObserveRequestDuration records an HTTP request duration for the latency histogram.
func ObserveRequestDuration(d time.Duration) {
	ms := d.Milliseconds()
	if ms < 0 {
		ms = 0
	}
	latencySumMS.Add(ms)
	for i, bound := range latencyBucketsMS {
		if ms <= bound {
			latencyCounts[i].Add(1)
			return
		}
	}
	latencyCounts[len(latencyBucketsMS)].Add(1)
}

// IncRequestError increments failed HTTP responses (5xx).
func IncRequestError() { requestErrors.Add(1) }

// IncQueryRun increments successful query executions.
func IncQueryRun() { queryRuns.Add(1) }

// IncQueryTimeout increments query timeout failures.
func IncQueryTimeout() { queryTimeouts.Add(1) }

// IncLLMCall increments governed LLM invocations.
func IncLLMCall() { llmCalls.Add(1) }

// IncLLMBudgetDenied increments LLM calls blocked by budget limits.
func IncLLMBudgetDenied() { llmBudgetDenials.Add(1) }

// AddLLMTokens adds estimated tokens consumed by LLM calls.
func AddLLMTokens(n int64) {
	if n > 0 {
		llmTokensTotal.Add(n)
	}
}

// IncSchedulerRun increments schedule run attempts.
func IncSchedulerRun() { schedulerRuns.Add(1) }

// IncSchedulerFailure increments failed schedule run attempts.
func IncSchedulerFailure() { schedulerFailures.Add(1) }

// IncScheduleDeadLetter increments schedule runs moved to dead-letter state.
func IncScheduleDeadLetter() { scheduleDead.Add(1) }

// IncScheduleLeaseRecovery increments expired schedule lease recoveries.
func IncScheduleLeaseRecovery() { leaseRecoveries.Add(1) }

// IncWebhookDelivery increments webhook delivery attempts.
func IncWebhookDelivery() { webhookDeliveries.Add(1) }

// IncWebhookFailure increments failed webhook delivery attempts.
func IncWebhookFailure() { webhookFailures.Add(1) }

// IncWebhookDeadLetter increments webhook deliveries moved to dead-letter state.
func IncWebhookDeadLetter() { webhookDead.Add(1) }

// IncAuthFailure increments authentication failures.
func IncAuthFailure() { authFailures.Add(1) }

// IncAuthzDenial increments authorization denials.
func IncAuthzDenial() { authzDenials.Add(1) }

// IncAuditWriteFailure increments failed audit or budget ledger writes.
func IncAuditWriteFailure() { auditWriteFails.Add(1) }

// IncWebhookRejected increments webhook deliveries blocked by policy.
func IncWebhookRejected() { webhookRejected.Add(1) }

// IncRateLimitStorageFailure increments distributed rate limiter storage (Postgres) failures,
// i.e. cases where the limiter could not reach its backing store and had to apply its
// configured failure mode (open/closed/local_fallback).
func IncRateLimitStorageFailure() { rateLimitStorageF.Add(1) }

// IncLLMBudgetFailClosed increments requests denied because the budget ledger
// was unavailable and fail-closed policy is active.
func IncLLMBudgetFailClosed() { llmBudgetFailClosed.Add(1) }

// IncLLMBudgetReservationExpired increments abandoned reservations reclaimed by cleanup.
func IncLLMBudgetReservationExpired(n int64) {
	if n > 0 {
		llmBudgetExpired.Add(n)
	}
}

// IncEmbedCall increments successful embedding invocations.
func IncEmbedCall() { embedCalls.Add(1) }

// IncEmbedDenied increments embedding calls blocked by governance policy.
func IncEmbedDenied() { embedDenials.Add(1) }

// IncEmbedError increments embedding provider/dimension/timeout failures.
func IncEmbedError() { embedErrors.Add(1) }

// ObserveEmbedDuration records embedding call latency for average reporting.
func ObserveEmbedDuration(d time.Duration) {
	ms := d.Milliseconds()
	if ms < 0 {
		ms = 0
	}
	embedLatencySumMS.Add(ms)
	embedLatencyCount.Add(1)
}

// PrometheusPoolMetrics renders pool statistics in Prometheus text exposition format.
func PrometheusPoolMetrics(version string, pool *pgxpool.Pool) string {
	return PrometheusAllPoolMetrics(version, []PoolMetric{{Name: "app", Role: "app", Pool: pool}})
}

// PrometheusAllPoolMetrics renders pool statistics for every configured pool.
func PrometheusAllPoolMetrics(version string, pools []PoolMetric) string {
	var b strings.Builder
	b.WriteString("# HELP pgqn_info Application build info.\n")
	b.WriteString("# TYPE pgqn_info gauge\n")
	b.WriteString(fmt.Sprintf("pgqn_info{version=%q} 1\n", version))
	writePoolGaugeHeader(&b, "pgqn_pool_acquired_conns")
	writePoolGaugeHeader(&b, "pgqn_pool_idle_conns")
	writePoolGaugeHeader(&b, "pgqn_pool_total_conns")
	writePoolGaugeHeader(&b, "pgqn_pool_max_conns")
	var acquiredTotal, idleTotal, totalConns, maxConns float64
	for _, item := range pools {
		if item.Pool == nil {
			continue
		}
		stat := item.Pool.Stat()
		labels := fmt.Sprintf(`{pool=%q,role=%q}`, item.Name, item.Role)
		acquired := float64(stat.AcquiredConns())
		idle := float64(stat.IdleConns())
		total := float64(stat.TotalConns())
		maximum := float64(stat.MaxConns())
		acquiredTotal += acquired
		idleTotal += idle
		totalConns += total
		maxConns += maximum
		writeMetricValue(&b, "pgqn_pool_acquired_conns", labels, acquired)
		writeMetricValue(&b, "pgqn_pool_idle_conns", labels, idle)
		writeMetricValue(&b, "pgqn_pool_total_conns", labels, total)
		writeMetricValue(&b, "pgqn_pool_max_conns", labels, maximum)
	}
	writeMetricValue(&b, "pgqn_pool_acquired_conns", `{pool="all",role="all"}`, acquiredTotal)
	writeMetricValue(&b, "pgqn_pool_idle_conns", `{pool="all",role="all"}`, idleTotal)
	writeMetricValue(&b, "pgqn_pool_total_conns", `{pool="all",role="all"}`, totalConns)
	writeMetricValue(&b, "pgqn_pool_max_conns", `{pool="all",role="all"}`, maxConns)
	writeCounter(&b, "pgqn_http_requests_total", requestTotal.Load())
	writeCounter(&b, "pgqn_http_errors_total", requestErrors.Load())
	writeLatencyHistogram(&b)
	writeCounter(&b, "pgqn_query_runs_total", queryRuns.Load())
	writeCounter(&b, "pgqn_query_timeouts_total", queryTimeouts.Load())
	writeCounter(&b, "pgqn_llm_calls_total", llmCalls.Load())
	writeCounter(&b, "pgqn_llm_budget_denials_total", llmBudgetDenials.Load())
	writeCounter(&b, "pgqn_llm_tokens_total", llmTokensTotal.Load())
	writeCounter(&b, "pgqn_scheduler_runs_total", schedulerRuns.Load())
	writeCounter(&b, "pgqn_scheduler_failures_total", schedulerFailures.Load())
	writeCounter(&b, "pgqn_schedule_dead_letters_total", scheduleDead.Load())
	writeCounter(&b, "pgqn_schedule_lease_recoveries_total", leaseRecoveries.Load())
	writeCounter(&b, "pgqn_webhook_deliveries_total", webhookDeliveries.Load())
	writeCounter(&b, "pgqn_webhook_failures_total", webhookFailures.Load())
	writeCounter(&b, "pgqn_webhook_dead_letters_total", webhookDead.Load())
	writeCounter(&b, "pgqn_auth_failures_total", authFailures.Load())
	writeCounter(&b, "pgqn_authz_denials_total", authzDenials.Load())
	writeCounter(&b, "pgqn_audit_write_failures_total", auditWriteFails.Load())
	writeCounter(&b, "pgqn_webhook_rejections_total", webhookRejected.Load())
	writeCounter(&b, "pgqn_rate_limit_storage_failures_total", rateLimitStorageF.Load())
	writeCounter(&b, "pgqn_llm_budget_fail_closed_total", llmBudgetFailClosed.Load())
	writeCounter(&b, "pgqn_llm_budget_reservations_expired_total", llmBudgetExpired.Load())
	writeCounter(&b, "pgqn_embed_calls_total", embedCalls.Load())
	writeCounter(&b, "pgqn_embed_denials_total", embedDenials.Load())
	writeCounter(&b, "pgqn_embed_errors_total", embedErrors.Load())
	if n := embedLatencyCount.Load(); n > 0 {
		avg := float64(embedLatencySumMS.Load()) / float64(n)
		b.WriteString("# TYPE pgqn_embed_latency_avg_ms gauge\n")
		b.WriteString(fmt.Sprintf("pgqn_embed_latency_avg_ms %g\n", avg))
	}
	return b.String()
}

func writePoolGaugeHeader(b *strings.Builder, name string) {
	b.WriteString("# TYPE ")
	b.WriteString(name)
	b.WriteString(" gauge\n")
}

func writeMetricValue(b *strings.Builder, name, labels string, value float64) {
	b.WriteString(fmt.Sprintf("%s%s %g\n", name, labels, value))
}

func writeCounter(b *strings.Builder, name string, value int64) {
	b.WriteString("# TYPE ")
	b.WriteString(name)
	b.WriteString(" counter\n")
	b.WriteString(fmt.Sprintf("%s %d\n", name, value))
}

func writeLatencyHistogram(b *strings.Builder) {
	b.WriteString("# HELP pgqn_http_request_duration_seconds HTTP request latency.\n")
	b.WriteString("# TYPE pgqn_http_request_duration_seconds histogram\n")
	var cumulative int64
	for i, bound := range latencyBucketsMS {
		cumulative += latencyCounts[i].Load()
		b.WriteString(fmt.Sprintf("pgqn_http_request_duration_seconds_bucket{le=\"%g\"} %d\n", float64(bound)/1000.0, cumulative))
	}
	cumulative += latencyCounts[len(latencyBucketsMS)].Load()
	b.WriteString(fmt.Sprintf("pgqn_http_request_duration_seconds_bucket{le=\"+Inf\"} %d\n", cumulative))
	b.WriteString(fmt.Sprintf("pgqn_http_request_duration_seconds_sum %g\n", float64(latencySumMS.Load())/1000.0))
	b.WriteString(fmt.Sprintf("pgqn_http_request_duration_seconds_count %d\n", cumulative))
}
