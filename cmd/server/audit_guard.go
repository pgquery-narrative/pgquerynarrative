package main

import (
	"context"

	"github.com/pgquerynarrative/pgquerynarrative/api/gen/queries"
	"github.com/pgquerynarrative/pgquerynarrative/api/gen/reports"
	"github.com/pgquerynarrative/pgquerynarrative/app/audit"
	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	goa "goa.design/goa/v3/pkg"
)

// auditGuardQueries records a RUN_QUERY audit entry, marked HighRisk, before invoking the
// "run" action of the queries service. In audit.ModeRequired, a failed audit write returns an
// error here and the query never executes (fail closed instead of running unaudited). Other
// queries-service actions (saved query CRUD, explain, stats) pass through unaffected.
func auditGuardQueries(store *audit.Store) func(goa.Endpoint) goa.Endpoint {
	return func(next goa.Endpoint) goa.Endpoint {
		return func(ctx context.Context, req any) (any, error) {
			if _, ok := req.(*queries.RunQueryPayload); ok && store != nil {
				if err := recordHighRiskAttempt(ctx, store, audit.EventRunQuery); err != nil {
					return nil, err
				}
			}
			return next(ctx, req)
		}
	}
}

// auditGuardReports records a GENERATE_REPORT audit entry, marked HighRisk, before invoking
// the "generate" action of the reports service, with the same fail-closed semantics as
// auditGuardQueries.
func auditGuardReports(store *audit.Store) func(goa.Endpoint) goa.Endpoint {
	return func(next goa.Endpoint) goa.Endpoint {
		return func(ctx context.Context, req any) (any, error) {
			if _, ok := req.(*reports.GenerateReportPayload); ok && store != nil {
				if err := recordHighRiskAttempt(ctx, store, audit.EventGenerateReport); err != nil {
					return nil, err
				}
			}
			return next(ctx, req)
		}
	}
}

func recordHighRiskAttempt(ctx context.Context, store *audit.Store, eventType string) error {
	p := auth.PrincipalFromContext(ctx)
	return store.Record(ctx, audit.Entry{
		EventType: eventType,
		UserID:    p.UserID,
		OrgID:     p.OrgID,
		HighRisk:  true,
	})
}
