package observability

import (
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PrometheusPoolMetrics renders pool statistics in Prometheus text exposition format.
func PrometheusPoolMetrics(version string, pool *pgxpool.Pool) string {
	var b strings.Builder
	b.WriteString("# HELP pgqn_info Application build info.\n")
	b.WriteString("# TYPE pgqn_info gauge\n")
	b.WriteString(fmt.Sprintf("pgqn_info{version=%q} 1\n", version))
	if pool == nil {
		return b.String()
	}
	stat := pool.Stat()
	writeGauge(&b, "pgqn_pool_acquired_conns", float64(stat.AcquiredConns()))
	writeGauge(&b, "pgqn_pool_idle_conns", float64(stat.IdleConns()))
	writeGauge(&b, "pgqn_pool_total_conns", float64(stat.TotalConns()))
	writeGauge(&b, "pgqn_pool_max_conns", float64(stat.MaxConns()))
	return b.String()
}

func writeGauge(b *strings.Builder, name string, value float64) {
	b.WriteString("# TYPE ")
	b.WriteString(name)
	b.WriteString(" gauge\n")
	b.WriteString(fmt.Sprintf("%s %g\n", name, value))
}
