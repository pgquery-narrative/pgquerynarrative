package service

import (
	"github.com/pgquerynarrative/pgquerynarrative/internal/auth"
	"github.com/pgquerynarrative/pgquerynarrative/internal/llm"
	"github.com/pgquerynarrative/pgquerynarrative/internal/queryrunner"
	"github.com/pgquerynarrative/pgquerynarrative/internal/security"
)

var (
	_ QueryExecutor        = (*queryrunner.Runner)(nil)
	_ LLMAuditSink         = (*llm.AuditStore)(nil)
	_ WebhookDeliverer     = (*security.WebhookClient)(nil)
	_ GovernedAI           = (*llm.GovernedClient)(nil)
	_ ConnectionAuthorizer = (*auth.ConnectionAuthorizer)(nil)
)
