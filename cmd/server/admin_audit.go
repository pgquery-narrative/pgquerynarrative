package main

import (
	"net/http"

	"github.com/pgquerynarrative/pgquerynarrative/app/audit"
)

// recordAdminAudit writes a high-risk admin audit entry and fails the request when
// required audit mode cannot persist it (fail closed).
func recordAdminAudit(w http.ResponseWriter, r *http.Request, deps adminDeps, entry audit.Entry) bool {
	if deps.auditStore == nil {
		return true
	}
	entry.HighRisk = true
	if err := deps.auditStore.Record(r.Context(), entry); err != nil {
		http.Error(w, "audit required but failed: "+err.Error(), http.StatusServiceUnavailable)
		return false
	}
	return true
}
