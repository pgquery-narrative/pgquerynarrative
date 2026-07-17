package service

import (
	"github.com/pgquerynarrative/pgquerynarrative/app/llm"
	"github.com/pgquerynarrative/pgquerynarrative/app/queryrunner"
	"github.com/pgquerynarrative/pgquerynarrative/app/security"
)

var (
	_ QueryExecutor      = (*queryrunner.Runner)(nil)
	_ LLMAuditSink       = (*llm.AuditStore)(nil)
	_ WebhookDeliverer   = (*security.WebhookClient)(nil)
)
