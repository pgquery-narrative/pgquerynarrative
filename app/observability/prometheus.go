package observability

import (
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgquerynarrative/pgquerynarrative/app/db"
)

var (
	requestTotal      atomic.Int64
	requestErrors     atomic.Int64
	queryRuns         atomic.Int64
	queryTimeouts     atomic.Int64
	llmCalls          atomic.Int64
	schedulerRuns     atomic.Int64
	webhookDeliveries atomic.Int64
	authFailures      atomic.Int64
	authzDenials      atomic.Int64
	llmBudgetDenials  atomic.Int64
	llmTokensTotal    atomic.Int64
	auditWriteFails   atomic.Int64
	webhookRejected   atomic.Int64
)

// IncRequest increments total HTTP requests observed by middleware.
func IncRequest() { requestTotal.Add(1) }

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

// IncWebhookDelivery increments webhook delivery attempts.
func IncWebhookDelivery() { webhookDeliveries.Add(1) }

// IncAuthFailure increments authentication failures.
func IncAuthFailure() { authFailures.Add(1) }

// IncAuthzDenial increments authorization denials.
func IncAuthzDenial() { authzDenials.Add(1) }

// IncAuditWriteFailure increments failed audit or budget ledger writes.
func IncAuditWriteFailure() { auditWriteFails.Add(1) }

// IncWebhookRejected increments webhook deliveries blocked by policy.
func IncWebhookRejected() { webhookRejected.Add(1) }

// PrometheusPoolMetrics renders pool statistics in Prometheus text exposition format.
func PrometheusPoolMetrics(version string, pool *pgxpool.Pool) string {
	return PrometheusAllPoolMetrics(version, []db.NamedPool{{Name: "app", Role: "app", Pool: pool}})
}

// PrometheusAllPoolMetrics renders pool statistics for every configured pool.
func PrometheusAllPoolMetrics(version string, pools []db.NamedPool) string {
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
	writeCounter(&b, "pgqn_query_runs_total", queryRuns.Load())
	writeCounter(&b, "pgqn_query_timeouts_total", queryTimeouts.Load())
	writeCounter(&b, "pgqn_llm_calls_total", llmCalls.Load())
	writeCounter(&b, "pgqn_llm_budget_denials_total", llmBudgetDenials.Load())
	writeCounter(&b, "pgqn_llm_tokens_total", llmTokensTotal.Load())
	writeCounter(&b, "pgqn_scheduler_runs_total", schedulerRuns.Load())
	writeCounter(&b, "pgqn_webhook_deliveries_total", webhookDeliveries.Load())
	writeCounter(&b, "pgqn_auth_failures_total", authFailures.Load())
	writeCounter(&b, "pgqn_authz_denials_total", authzDenials.Load())
	writeCounter(&b, "pgqn_audit_write_failures_total", auditWriteFails.Load())
	writeCounter(&b, "pgqn_webhook_rejections_total", webhookRejected.Load())
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
