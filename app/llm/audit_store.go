package llm

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/app/db"
	"github.com/pgquerynarrative/pgquerynarrative/app/observability"
)

// AuditEvent records one governed LLM invocation.
type AuditEvent struct {
	OrganizationID   string
	UserID           string
	Provider         string
	Model            string
	Operation        string
	PolicyDecision   PolicyDecision
	DataClasses      []string
	SendRowData      bool
	RedactPII        bool
	AllowExternal    bool
	PromptTokens     int
	CompletionTokens int
	LatencyMs        int
	EstimatedCostUSD float64
}

// AuditStore persists LLM audit events.
type AuditStore struct {
	pool *pgxpool.Pool
}

// NewAuditStore creates an LLM audit store backed by PostgreSQL.
func NewAuditStore(pool *pgxpool.Pool) *AuditStore {
	if pool == nil {
		return nil
	}
	return &AuditStore{pool: pool}
}

// Record inserts an LLM audit event. Failures are ignored so LLM calls are not blocked by audit outages.
func (s *AuditStore) Record(ctx context.Context, ev AuditEvent) {
	if s == nil || s.pool == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	err := db.ExecWithOrg(ctx, s.pool, ev.OrganizationID, `
		INSERT INTO app.llm_audit_events (
			organization_id, user_id, provider, model, operation, policy_decision,
			data_classes, send_row_data, redact_pii, allow_external,
			prompt_tokens, completion_tokens, estimated_cost_usd, latency_ms
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
	`, ev.OrganizationID, ev.UserID, ev.Provider, ev.Model, ev.Operation, string(ev.PolicyDecision),
		ev.DataClasses, ev.SendRowData, ev.RedactPII, ev.AllowExternal,
		ev.PromptTokens, ev.CompletionTokens, ev.EstimatedCostUSD, ev.LatencyMs)
	if err != nil {
		observability.IncAuditWriteFailure()
	}
}
