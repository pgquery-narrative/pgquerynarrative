package service

import (
	"context"

	"github.com/pgquerynarrative/pgquerynarrative/app/llm"
	"github.com/pgquerynarrative/pgquerynarrative/app/queryrunner"
	"github.com/pgquerynarrative/pgquerynarrative/app/security"
)

// ConnectionAuthorizer decides whether a principal may use a data connection
// for a given action (see app/auth's Action* constants). It is the narrow
// seam QueriesService, ReportsService, SchemaService, AskService,
// SchedulesService, and ConnectionsService depend on via SetAuthorizer, so
// call sites never need the concrete app pool the real implementation
// (auth.NewConnectionAuthorizer) is backed by. A nil ConnectionAuthorizer is
// permissive (see checkConnectionAccess), matching the concrete type's
// nil-receiver behavior. Like GovernedAI, this is wired once at construction
// time from narrative.NewClient and must not be reassigned afterward.
type ConnectionAuthorizer interface {
	// AuthorizeConnection returns nil if the principal (orgID, userID, role)
	// may perform action on connectionID, or an error (wrapping
	// auth.ErrConnectionForbidden) otherwise.
	AuthorizeConnection(ctx context.Context, orgID, userID, role, connectionID, action string) error
	// AllowedConnections filters configured down to the connection IDs the
	// principal may use for action (used by ConnectionsService.List).
	AllowedConnections(ctx context.Context, orgID, userID, role string, configured []string, action string) []string
}

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

// GovernedAI invokes an LLM under audit, budget, and external-data governance
// policy. It is the narrow seam ReportsService, AskService, and narrative
// generation depend on so call sites never need direct access to the raw
// provider client, audit store, or budget store — those are wired once, at
// construction time (see llm.NewGovernedClient, called only from
// narrative.NewClient), by whoever builds the GovernedAI.
type GovernedAI interface {
	// Invoke evaluates governance policy for gov, checks/reserves budget,
	// invokes the LLM provider, and records an audit event. operation
	// identifies the call site (e.g. "narrative_generate", "nl2sql") for
	// audit/observability.
	Invoke(ctx context.Context, operation string, gov llm.GovernanceInput, prompt string) (string, error)
	// Provider returns the underlying LLM provider name (e.g. "ollama", "claude").
	Provider() string
	// AllowExternalData reports whether this invocation path may send data to
	// external/cloud LLM providers.
	AllowExternalData() bool
}
