package service

import (
	"context"

	"github.com/pgquerynarrative/pgquerynarrative/app/llm"
	"github.com/pgquerynarrative/pgquerynarrative/app/queryrunner"
	"github.com/pgquerynarrative/pgquerynarrative/app/security"
)

// QueryExecutor runs validated read-only SQL and returns tabular results.
type QueryExecutor interface {
	Run(ctx context.Context, sql string, limit int) (*queryrunner.Result, error)
}

// LLMAuditSink persists governed LLM invocation audit records.
type LLMAuditSink interface {
	Record(ctx context.Context, ev llm.AuditEvent)
}

// WebhookDeliverer posts signed schedule webhook payloads.
type WebhookDeliverer interface {
	PostJSON(ctx context.Context, destination, deliveryID string, payload map[string]any) (*security.DeliveryResult, error)
}

// ScheduleRunner executes due schedules and records run outcomes.
type ScheduleRunner interface {
	RunDue(ctx context.Context) error
}
