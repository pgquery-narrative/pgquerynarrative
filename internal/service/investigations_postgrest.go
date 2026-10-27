package service

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/pgquerynarrative/pgquerynarrative/api/gen/investigations"
	"github.com/pgquerynarrative/pgquerynarrative/api/gen/queries"
	apperrors "github.com/pgquerynarrative/pgquerynarrative/internal/errors"
	"github.com/pgquerynarrative/pgquerynarrative/internal/queryrunner"
	"github.com/pgquerynarrative/pgquerynarrative/internal/story"
)

// SetPostgRESTReplayRoles sets the PostgREST roles an imported request may be
// replayed as (SET LOCAL ROLE; never request.jwt.claims — see ReplayContext).
// Empty: never impersonate. Intended to be called once, from narrative.NewClient.
func (s *InvestigationsService) SetPostgRESTReplayRoles(roles []string) {
	s.replayRoles = map[string]bool{}
	for _, r := range roles {
		if r = strings.TrimSpace(r); r != "" {
			s.replayRoles[r] = true
		}
	}
}

func postgrestValidationError(msg string) error {
	return &investigations.ValidationError{Name: "validation_error", Message: msg, Code: strPtr("VALIDATION_ERROR")}
}

// CreateFromPostgrest opens an investigation from one captured PostgREST read
// request. The PostgreSQL log excerpt supplies the statement, its bind values,
// the request context and the statement duration or failure. The URL is never
// used to infer SQL, and request headers and cookies are discarded during
// parsing. Every replay applies the captured context as far as this
// connection's policy allows (see postgrestReplay).
func (s *InvestigationsService) CreateFromPostgrest(ctx context.Context, payload *investigations.CreateFromPostgRESTPayload) (*investigations.Investigation, error) {
	if payload.HTTPDurationMs != nil && (*payload.HTTPDurationMs < 0 || math.IsNaN(*payload.HTTPDurationMs) || math.IsInf(*payload.HTTPDurationMs, 0)) {
		return nil, postgrestValidationError("http_duration_ms must be finite and non-negative")
	}
	capture, err := queryrunner.ParsePostgRESTLog(payload.PostgresLog)
	if err != nil {
		return nil, postgrestValidationError(err.Error())
	}
	var serverTiming *float64
	if payload.ServerTiming != nil && *payload.ServerTiming != "" {
		d, ok := queryrunner.ServerTimingDuration(*payload.ServerTiming, "transaction")
		if !ok {
			return nil, postgrestValidationError("server_timing has no valid transaction;dur=<ms> metric; send the Server-Timing header from the same request (PostgREST server-timing-enabled=true) or omit it")
		}
		serverTiming = &d
	}

	connID := "default"
	if payload.ConnectionID != nil && *payload.ConnectionID != "" {
		connID = *payload.ConnectionID
	}
	replay, err := s.postgrestReplay(ctx, &connID, capture)
	if err != nil {
		return nil, err
	}
	appliedPath := s.queriesSvc.PostgRESTReplaySearchPath(ctx, &connID, replay.SearchPath)

	pr := &investigations.PostgRESTRequest{
		Method:                    capture.Method,
		Endpoint:                  capture.Endpoint,
		Role:                      capture.Role,
		StatementDurationMs:       capture.StatementMs,
		HTTPDurationMs:            payload.HTTPDurationMs,
		ServerTimingTransactionMs: serverTiming,
		SessionContextMissing:     boolPtrIf(!capture.ContextLogged),
		PreparedStatement:         capture.Prepared,
		RequestSettings:           capture.Settings,
		ReplayLimitations:         postgrestReplayLimitations(capture, replay, appliedPath),
	}
	if capture.ContextLogged {
		pr.SearchPath = &capture.SearchPath
	}
	if capture.QueryID != "" {
		pr.QueryID = &capture.QueryID
	}
	if capture.Failure != "" {
		pr.Failure = &capture.Failure
	}
	if replay.Role != "" {
		pr.ReplayRole = &replay.Role
	}
	if len(appliedPath) > 0 {
		path := quoteSearchPath(appliedPath)
		pr.ReplaySearchPath = &path
	}

	inv, err := s.insertInvestigation(queryrunner.WithReplayContext(ctx, replay), investigationDraft{
		Title:        postgrestTitle(payload.Title, capture),
		SQL:          capture.SQL,
		ConnectionID: connID,
		Analyze:      payload.Analyze,
		Binds:        capture.Binds,
		PostgREST:    pr,
	})
	var ve *investigations.ValidationError
	if errors.As(err, &ve) && ve.Message == apperrors.ErrQueryTooLong.Error() {
		literal, _ := planSQL(capture.SQL, capture.Binds)
		ve.Message = fmt.Sprintf("the captured statement with its values substituted is %d bytes, over this connection's query length limit; raise DATABASE_MAX_QUERY_LENGTH (default 10000) to import it", len(literal))
	}
	return inv, err
}

func postgrestTitle(requested *string, c *queryrunner.PostgRESTCapture) string {
	if requested != nil && *requested != "" {
		return *requested
	}
	if c.ContextLogged {
		return fmt.Sprintf("PostgREST %s %s", c.Method, c.Endpoint)
	}
	targets, _ := queryrunner.CollectFunctions(c.SQL)
	if rels, _ := queryrunner.CollectRelations(c.SQL); len(rels) > 0 {
		targets = append(targets, rels...)
	}
	return "PostgREST request reading " + strings.Join(targets, ", ")
}

// postgrestReplay builds the context every replay of a capture runs in: the
// captured search_path (limited to this connection's allowed schemas by the
// runner), the captured settings, the request method and path, and — only for
// a role an operator allowlisted and this connection may independently assume
// via a real Postgres GRANT — the captured role. request.jwt.claims is never
// replayed, for any role: the capture is caller-supplied log text with no
// verification it is authentic, so a forged claims value would let any
// principal who can create a PostgREST-imported investigation pick the
// identity an RLS policy keyed on it (for example auth.uid()) evaluates for.
// A real GRANT is not forgeable the same way: Postgres itself checks it.
func (s *InvestigationsService) postgrestReplay(ctx context.Context, connID *string, c *queryrunner.PostgRESTCapture) (*queryrunner.ReplayContext, error) {
	rc := &queryrunner.ReplayContext{
		SearchPath: queryrunner.ParseSearchPath(c.SearchPath),
		Settings:   c.Settings,
		Method:     c.Method,
		Path:       c.Endpoint,
	}
	if !c.ContextLogged || !s.replayRoles[c.Role] {
		return rc, nil
	}
	if err := s.checkReplayRoleGrant(ctx, connID, c.Role, func(detail string) error {
		return postgrestValidationError(fmt.Sprintf("PostgREST role %q is allowlisted for replay (SECURITY_POSTGREST_REPLAY_ROLES), but this connection cannot assume it; grant it to the analytical role (GRANT %s TO <analytical role>). %s", c.Role, pgx.Identifier{c.Role}.Sanitize(), detail))
	}); err != nil {
		return nil, err
	}
	rc.Role = c.Role
	return rc, nil
}

// checkReplayRoleGrant verifies role's real Postgres GRANT
// (CheckPostgRESTReplayRole) — not just whether it is *configured* in
// SECURITY_POSTGREST_REPLAY_ROLES, which every caller already checks itself
// before calling this. A revoked or missing grant is reported through
// actionable, which turns PostgreSQL's own detail (via
// executionFailedMessage — deliberately not ClassifyRunError/SanitizeAPIError,
// which are shared by every other query path in the product and must stay
// generic, see runerror.go) into a context-specific remediation message; any
// other shaped error (e.g. a validation error already built upstream) passes
// through normalizeInvestigationError unchanged. Shared by postgrestReplay
// (import time) and postgrestReplayContext (every later RankCandidates/
// AddCandidate), whose messages differ only in wording appropriate to when
// each runs.
func (s *InvestigationsService) checkReplayRoleGrant(ctx context.Context, connID *string, role string, actionable func(detail string) error) error {
	err := s.queriesSvc.CheckPostgRESTReplayRole(ctx, connID, role)
	if err == nil {
		return nil
	}
	var qv *queries.ValidationError
	if !errors.As(err, &qv) {
		return actionable(executionFailedMessage(err))
	}
	return normalizeInvestigationError(err)
}

// postgrestReplayContext rebuilds the replay context stored with inv, so a
// ranking or comparison runs in the same context as the imported evidence.
func (s *InvestigationsService) postgrestReplayContext(ctx context.Context, inv *investigations.Investigation) (context.Context, error) {
	pr := inv.PostgrestRequest
	if pr == nil {
		return ctx, nil
	}
	rc := &queryrunner.ReplayContext{Settings: pr.RequestSettings, Method: pr.Method, Path: pr.Endpoint}
	if pr.ReplaySearchPath != nil {
		rc.SearchPath = queryrunner.ParseSearchPath(*pr.ReplaySearchPath)
	}
	if pr.ReplayRole != nil && *pr.ReplayRole != "" {
		role := *pr.ReplayRole
		if !s.replayRoles[role] {
			return nil, postgrestValidationError(fmt.Sprintf("this investigation's evidence was replayed as PostgREST role %q, which is no longer allowlisted in SECURITY_POSTGREST_REPLAY_ROLES; re-import the request to compare under the connection's role", role))
		}
		// The allowlist check above only says the role is still *configured*.
		// It says nothing about whether the underlying Postgres GRANT this
		// connection needs to assume it is still there — an admin can revoke
		// that independently of the config file. Without this, a later
		// RankCandidates/AddCandidate on this same investigation would only
		// discover the revoked grant deep inside Explain/ComparePlans' own
		// "SET LOCAL ROLE" call, surfacing a bare, generic execution-failed
		// error (see runerror.go) instead of postgrestReplay's own clear,
		// actionable remediation message for the identical condition at
		// import time.
		if err := s.checkReplayRoleGrant(ctx, &inv.ConnectionID, role, func(detail string) error {
			return postgrestValidationError(fmt.Sprintf("this investigation's evidence was replayed as PostgREST role %q, but this connection can no longer assume it; grant it to the analytical role (GRANT %s TO <analytical role>) or re-import the request to compare under the connection's role. %s", role, pgx.Identifier{role}.Sanitize(), detail))
		}); err != nil {
			return nil, err
		}
		rc.Role = role
	}
	return queryrunner.WithReplayContext(ctx, rc), nil
}

func quoteSearchPath(schemas []string) string {
	quoted := make([]string, len(schemas))
	for i, s := range schemas {
		quoted[i] = pgx.Identifier{s}.Sanitize()
	}
	return strings.Join(quoted, ", ")
}

// postgrestReplayLimitations lists what replaying a captured request does not
// reproduce. They are stored with the investigation and printed in its report,
// so a reader never mistakes a query-level result for an endpoint result.
func postgrestReplayLimitations(c *queryrunner.PostgRESTCapture, rc *queryrunner.ReplayContext, appliedPath []string) []string {
	var out []string
	switch {
	case !c.ContextLogged:
		out = append(out, "The request's session context was not logged (slow-query-only logging): its PostgREST role, method, path, search_path and JWT claims are unknown, and the replay uses the connection's role and settings. Log with log_min_duration_statement=0 or log_statement=all to capture them.")
	case rc.Role == "":
		out = append(out,
			fmt.Sprintf("Replay uses the connection's database role; captured PostgREST role %q is not impersonated (allowlist it in SECURITY_POSTGREST_REPLAY_ROLES and grant it to the analytical role to replay as it). Grants and RLS policies can differ.", c.Role),
			"Session claims (request.jwt.claims) and request headers are not replayed; RLS policies that read them can return different rows.")
	default:
		out = append(out, "Request headers, cookies and JWT claims (request.jwt.claims) are not replayed even under the impersonated role, because the capture is unverified log text; RLS policies keyed on claims (for example auth.uid()) can return different rows than the real request.")
	}
	if c.ContextLogged {
		var dropped []string
		for _, s := range queryrunner.ParseSearchPath(c.SearchPath) {
			if !slices.Contains(appliedPath, s) {
				dropped = append(dropped, s)
			}
		}
		if len(dropped) > 0 {
			searched := "only the connection's own search_path"
			if len(appliedPath) > 0 {
				searched = quoteSearchPath(appliedPath)
			}
			out = append(out, fmt.Sprintf("Captured search_path %s: the replay searches %s; %s %s outside this connection's allowed schemas, so functions, operators and types found only there (computed columns, extension operators such as citext equality) can resolve differently or fail.", c.SearchPath, searched, strings.Join(dropped, ", "), pluralIsAre(len(dropped))))
		}
	}
	for _, name := range slices.Sorted(maps.Keys(c.Settings)) {
		if !queryrunner.ReplayableSetting(name) {
			out = append(out, fmt.Sprintf("The request ran with %s=%s; replays use the product's own timeout so a statement that timed out in production can still be measured.", name, c.Settings[name]))
		}
	}
	if c.OtherSettings > 0 {
		out = append(out, fmt.Sprintf("This request set %d other setting(s), such as per-role custom settings; their names and values are not stored and the replay does not apply them.", c.OtherSettings))
	}
	return append(out, "The endpoint's response body is not replayed. Any result verification compares only candidate and source SQL under the captured binds and the replay context.")
}

func pluralIsAre(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

// modelPlanCache records whether PostgreSQL is likely to run the captured
// statement with the custom plan the replay measured (this request's values)
// or with its generic plan. A named prepared statement switches to the
// generic plan after five executions on a connection when the generic plan's
// cost is below the AVERAGE of those first five custom plans' costs plus a
// planning allowance of 1000 * cpu_operator_cost * (range table entries + 1)
// — an average this function cannot know from a single captured request, so
// it draws a deterministic conclusion only where the captured
// plan_cache_mode or prepared-statement-off shape makes one certain
// regardless of other traffic on the connection (force_custom_plan,
// force_generic_plan, no parameters, unnamed statement); plan_cache_mode=auto
// (the default) reports this capture's own costs without predicting which
// way the five-execution average will land.
func (s *InvestigationsService) modelPlanCache(ctx context.Context, d investigationDraft, custom *queries.ExplainQueryResult) {
	pr := d.PostgREST
	note := func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		pr.PlanCacheNote = &msg
	}
	mode := strings.ToLower(pr.RequestSettings["plan_cache_mode"])
	switch {
	case mode == "force_custom_plan":
		note("plan_cache_mode=force_custom_plan: PostgreSQL plans every execution for its own values, so the measured custom plan is the production plan.")
		return
	case len(d.Binds) == 0:
		note("The statement has no parameters, so its custom and generic plans are the same.")
		return
	case pr.PreparedStatement == nil:
		note("Whether this ran as a named prepared statement is unknown: it was recovered only from the error context, which carries bind values but not the statement's name. Plan-cache behavior cannot be judged from this capture.")
		return
	case !*pr.PreparedStatement:
		note("PostgREST ran this as an unnamed statement (prepared statements off), so PostgreSQL plans every execution for its own values; the measured custom plan is the production plan.")
		return
	}
	generic, err := s.queriesSvc.ExplainPlan(ctx, &queries.ExplainQueryPayload{SQL: d.SQL, ConnectionID: &d.ConnectionID})
	if err != nil || generic.GenericPlan == nil || !*generic.GenericPlan {
		note("The generic plan could not be evaluated (EXPLAIN (GENERIC_PLAN) needs PostgreSQL 16 or later); after five executions on a connection PostgreSQL may run a generic plan that differs from the measured one.")
		return
	}
	g, c := generic.TotalCost, custom.TotalCost
	pr.GenericPlanTotalCost = &g
	shape := "the same plan shape as"
	if !slices.Equal(planNodeTypes(generic.Plan), planNodeTypes(custom.Plan)) {
		shape = "a different plan shape from"
	}
	if mode == "force_generic_plan" {
		note("plan_cache_mode=force_generic_plan: production runs the generic plan (cost %.2f, %s the measured custom plan at %.2f). Review the generic plan before relying on the measured one.", g, shape, c)
		return
	}
	// plan_cache_mode=auto (the default, including when unset): PostgreSQL
	// decides by comparing the generic plan's cost to the AVERAGE of the
	// first five custom-plan costs on that connection — not any single
	// execution's. This capture has only one bind's custom-plan cost, so a
	// skewed earlier or later bind (unusually cheap or expensive relative to
	// the rest of the workload) can pull that five-execution average either
	// side of the generic plan's cost regardless of what this one capture
	// cost. Production's actual choice cannot be determined from a single
	// capture; only the two costs this capture can measure are reported.
	note("plan_cache_mode=auto: PostgreSQL decides this by comparing the generic plan's cost to the AVERAGE of the first five custom-plan costs on that connection, not any single request's. This capture gives only this bind's custom-plan cost (%.2f) against the generic plan's cost (%.2f, %s it); whether production keeps the custom plan or switches to the generic one after five executions cannot be determined from one capture.", c, g, shape)
}

// planNodeTypes lists an EXPLAIN (FORMAT JSON) plan's node types in pre-order.
func planNodeTypes(plan any) []string {
	var out []string
	var walk func(any)
	walk = func(node any) {
		m, ok := node.(map[string]any)
		if !ok {
			return
		}
		if t, ok := m["Node Type"].(string); ok {
			out = append(out, t)
		}
		children, _ := m["Plans"].([]any)
		for _, c := range children {
			walk(c)
		}
	}
	if list, ok := plan.([]any); ok && len(list) > 0 {
		if root, ok := list[0].(map[string]any); ok {
			walk(root["Plan"])
		}
	}
	return out
}

// investigationCompareSQL keeps imported parameters authoritative throughout
// comparison. Overrides would detach the proof from the captured request.
func investigationCompareSQL(inv *investigations.Investigation, payload *investigations.AddCandidatePayload) (string, string, []string, error) {
	if inv.PostgrestRequest == nil {
		return inv.SQL, payload.CandidateSQL, payload.Binds, nil
	}
	if len(payload.Binds) > 0 && !slices.Equal(payload.Binds, inv.Binds) {
		return "", "", nil, fmt.Errorf("PostgREST comparisons must use the captured bind values; import a new capture to change them")
	}
	// Do not silently fall back to a generic plan if captured evidence is missing.
	before, err := queryrunner.SubstituteCapturedParams(inv.SQL, inv.Binds)
	if err != nil {
		return "", "", nil, fmt.Errorf("captured source binds: %w", err)
	}
	after, err := queryrunner.SubstituteCapturedParams(payload.CandidateSQL, inv.Binds)
	if err != nil {
		return "", "", nil, fmt.Errorf("candidate binds: %w", err)
	}
	return before, after, inv.Binds, nil
}

// postgrestEvidenceForReport maps stored provenance to the report's PostgREST
// section. plannerCost is the EXPLAIN total cost (an estimate, not a time).
func postgrestEvidenceForReport(pr *investigations.PostgRESTRequest, plannerCost *float64, sql string, binds []string) *story.PostgRESTEvidence {
	return &story.PostgRESTEvidence{
		Binds:                     binds,
		Method:                    pr.Method,
		Endpoint:                  pr.Endpoint,
		Role:                      pr.Role,
		SearchPath:                pr.SearchPath,
		QueryID:                   pr.QueryID,
		HTTPDurationMs:            pr.HTTPDurationMs,
		ServerTimingTransactionMs: pr.ServerTimingTransactionMs,
		StatementDurationMs:       pr.StatementDurationMs,
		PlannerTotalCost:          plannerCost,
		SessionContextMissing:     pr.SessionContextMissing != nil && *pr.SessionContextMissing,
		Failure:                   pr.Failure,
		ReplayRole:                pr.ReplayRole,
		ReplaySearchPath:          pr.ReplaySearchPath,
		RequestSettings:           pr.RequestSettings,
		PlanCacheNote:             pr.PlanCacheNote,
		ReplayLimitations:         pr.ReplayLimitations,
		EndpointEquivalence:       "not_claimed",
		ApplyAt:                   applyAtForSQL(sql),
	}
}

// applyAtForSQL says where each kind of change would land for a PostgREST
// statement. The wrapper itself is never the target: the cost lives in the
// relation or function it reads, or in an index on that relation.
func applyAtForSQL(sql string) []string {
	out := []string{
		"Index: review DDL against the actual predicate and requested ordering/pagination. Measure it on a controlled database before relying on it; a recommendation or cost projection is not proof of faster HTTP responses or unchanged endpoint results.",
	}
	if rels, err := queryrunner.CollectRelations(sql); err == nil {
		for _, rel := range rels {
			out = append(out, fmt.Sprintf("Underlying relation %s: if it is a view, a predicate change belongs in the view definition. The generated wrapper is not a target.", rel))
		}
	}
	if fns, err := queryrunner.CollectFunctions(sql); err == nil {
		for _, fn := range fns {
			out = append(out, fmt.Sprintf("Function %s: a change to what it reads belongs in its definition; plans inside a function PostgreSQL does not inline are not visible here (auto_explain.log_nested_statements shows them).", fn))
		}
	}
	return append(out, "Request shape: a different filter (for example a range on a base column instead of a function of it) is a different URL and a different generated statement, so it needs its own captured investigation.")
}

// postgrestIndexNeedsRewriteNote qualifies index advice that assumes the
// function-wrapped predicate is rewritten: PostgREST generates the predicate
// from the request, so that rewrite is not available for this statement.
const postgrestIndexNeedsRewriteNote = "PostgREST: this index assumes a predicate rewrite that is not available here, because PostgREST generates the predicate from the request. As captured, this endpoint cannot use it; it needs a different request shape (for example a range filter on the base column, which is a different URL to capture separately)."
