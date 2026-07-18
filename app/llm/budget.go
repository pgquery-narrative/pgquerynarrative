package llm

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/app/db"
	"github.com/pgquerynarrative/pgquerynarrative/app/observability"
)

// defaultReservationTTL bounds how long a reservation counts against the
// budget before ExpireAbandoned reclaims it (e.g. the process crashed after
// reserving but before reconciling or releasing).
const defaultReservationTTL = 5 * time.Minute

// ErrBudgetLedgerUnavailable is returned by Reserve/Check when the ledger
// database cannot be reached and the store is configured to fail closed.
var ErrBudgetLedgerUnavailable = errors.New("LLM budget ledger unavailable")

// BudgetConfig limits LLM usage per organization and per user (daily and monthly).
type BudgetConfig struct {
	DailyTokenLimit          int     // 0 = unlimited
	DailyCostUSD             float64 // 0 = unlimited
	MonthlyTokenLimit        int     // 0 = unlimited
	MonthlyCostUSD           float64 // 0 = unlimited
	PerUserDailyTokenLimit   int     // 0 = unlimited
	PerUserDailyCostUSD      float64 // 0 = unlimited
	PerUserMonthlyTokenLimit int     // 0 = unlimited
	PerUserMonthlyCostUSD    float64 // 0 = unlimited
	USDPer1kTokens           float64 // cost estimate rate; default 0.002
	// FailClosed denies LLM calls when the budget ledger database cannot be
	// reached, instead of the legacy fail-open behavior. Should be true for
	// cloud providers and production/strict deployments so an outage cannot
	// be used to bypass spend controls.
	FailClosed bool
	// ReservationTTL bounds how long an unreconciled reservation counts
	// against the budget. Default 5 minutes.
	ReservationTTL time.Duration
}

// BudgetStore enforces and records org/user LLM budgets.
type BudgetStore struct {
	pool *pgxpool.Pool
	cfg  BudgetConfig
}

// NewBudgetStore creates a budget enforcer. Nil when pool is nil.
func NewBudgetStore(pool *pgxpool.Pool, cfg BudgetConfig) *BudgetStore {
	if pool == nil {
		return nil
	}
	if cfg.USDPer1kTokens <= 0 {
		cfg.USDPer1kTokens = 0.002
	}
	if cfg.ReservationTTL <= 0 {
		cfg.ReservationTTL = defaultReservationTTL
	}
	return &BudgetStore{pool: pool, cfg: cfg}
}

// Enabled reports whether any budget limit is configured.
func (b *BudgetStore) Enabled() bool {
	if b == nil || b.pool == nil {
		return false
	}
	c := b.cfg
	return c.DailyTokenLimit > 0 || c.DailyCostUSD > 0 ||
		c.MonthlyTokenLimit > 0 || c.MonthlyCostUSD > 0 ||
		c.PerUserDailyTokenLimit > 0 || c.PerUserDailyCostUSD > 0 ||
		c.PerUserMonthlyTokenLimit > 0 || c.PerUserMonthlyCostUSD > 0
}

// FailClosed reports whether ledger unavailability should deny calls.
func (b *BudgetStore) FailClosed() bool {
	return b != nil && b.cfg.FailClosed
}

// rowScanner is satisfied by both *pgxpool.Pool and pgx.Tx for QueryRow calls.
type rowScanner interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Check returns an error when org or user budgets would be exceeded. It
// accounts for both committed usage and currently active (unreconciled)
// reservations, so concurrent in-flight requests near the limit are blocked
// even before their actual usage is recorded. Prefer Reserve for the
// atomic reserve-before-call path; Check remains available for advisory
// (non-reserving) callers.
func (b *BudgetStore) Check(ctx context.Context, orgID, userID string, upcomingTokens int) error {
	if !b.Enabled() {
		return nil
	}
	orgID = normalizeOrgID(orgID)
	userID = normalizeUserID(userID)
	upcomingCost := b.EstimateCostUSD(upcomingTokens)
	return b.checkAllScopes(ctx, b.pool, orgID, userID, upcomingTokens, upcomingCost)
}

// checkAllScopes runs the four budget scope checks (org daily/monthly, user
// daily/monthly) against committed usage plus active reservations using q,
// which may be the pool (advisory Check) or a locked transaction (Reserve).
func (b *BudgetStore) checkAllScopes(ctx context.Context, q rowScanner, orgID, userID string, upcomingTokens int, upcomingCost float64) error {
	if err := b.checkScope(ctx, q, "org_daily", orgID, "", upcomingTokens, upcomingCost); err != nil {
		return err
	}
	if err := b.checkScope(ctx, q, "org_monthly", orgID, "", upcomingTokens, upcomingCost); err != nil {
		return err
	}
	if userID == "" {
		return nil
	}
	if err := b.checkScope(ctx, q, "user_daily", orgID, userID, upcomingTokens, upcomingCost); err != nil {
		return err
	}
	if err := b.checkScope(ctx, q, "user_monthly", orgID, userID, upcomingTokens, upcomingCost); err != nil {
		return err
	}
	return nil
}

func (b *BudgetStore) checkScope(ctx context.Context, q rowScanner, scope, orgID, userID string, upcomingTokens int, upcomingCost float64) error {
	tokenLimit, costLimit := b.limitsForScope(scope)
	if tokenLimit <= 0 && costLimit <= 0 {
		return nil
	}
	committedTokens, committedCost, err := b.committedUsage(ctx, q, scope, orgID, userID)
	if err != nil {
		return b.ledgerError(err)
	}
	reservedTokens, reservedCost, err := b.activeReservations(ctx, q, orgID, userID)
	if err != nil {
		return b.ledgerError(err)
	}
	totalTokens := committedTokens + reservedTokens + int64(upcomingTokens)
	totalCost := committedCost + reservedCost + upcomingCost
	if tokenLimit > 0 && totalTokens > int64(tokenLimit) {
		return fmt.Errorf("LLM %s token budget exceeded (%d/%d, includes %d reserved)", scope, totalTokens, tokenLimit, reservedTokens)
	}
	if costLimit > 0 && totalCost > costLimit {
		return fmt.Errorf("LLM %s cost budget exceeded (%.4f/%.4f USD, includes %.4f reserved)", scope, totalCost, costLimit, reservedCost)
	}
	return nil
}

func (b *BudgetStore) limitsForScope(scope string) (tokenLimit int, costLimit float64) {
	switch scope {
	case "org_daily":
		return b.cfg.DailyTokenLimit, b.cfg.DailyCostUSD
	case "org_monthly":
		return b.cfg.MonthlyTokenLimit, b.cfg.MonthlyCostUSD
	case "user_daily":
		return b.cfg.PerUserDailyTokenLimit, b.cfg.PerUserDailyCostUSD
	case "user_monthly":
		return b.cfg.PerUserMonthlyTokenLimit, b.cfg.PerUserMonthlyCostUSD
	default:
		return 0, 0
	}
}

// committedUsage returns already-recorded usage (from RecordUsage/ReconcileUsage) for a scope.
func (b *BudgetStore) committedUsage(ctx context.Context, q rowScanner, scope, orgID, userID string) (int64, float64, error) {
	var tokens int64
	var cost float64
	var row pgx.Row
	switch scope {
	case "org_daily":
		row = q.QueryRow(ctx, `
			SELECT COALESCE(prompt_tokens + completion_tokens, 0), COALESCE(estimated_cost_usd, 0)
			FROM app.llm_budget_usage
			WHERE organization_id = $1::uuid AND usage_date = CURRENT_DATE
		`, orgID)
	case "org_monthly":
		row = q.QueryRow(ctx, `
			SELECT COALESCE(SUM(prompt_tokens + completion_tokens), 0), COALESCE(SUM(estimated_cost_usd), 0)
			FROM app.llm_budget_usage
			WHERE organization_id = $1::uuid AND usage_date >= date_trunc('month', CURRENT_DATE)::date
		`, orgID)
	case "user_daily":
		row = q.QueryRow(ctx, `
			SELECT COALESCE(prompt_tokens + completion_tokens, 0), COALESCE(estimated_cost_usd, 0)
			FROM app.llm_user_budget_usage
			WHERE organization_id = $1::uuid AND user_id = $2 AND usage_date = CURRENT_DATE
		`, orgID, userID)
	case "user_monthly":
		row = q.QueryRow(ctx, `
			SELECT COALESCE(SUM(prompt_tokens + completion_tokens), 0), COALESCE(SUM(estimated_cost_usd), 0)
			FROM app.llm_user_budget_usage
			WHERE organization_id = $1::uuid AND user_id = $2 AND usage_date >= date_trunc('month', CURRENT_DATE)::date
		`, orgID, userID)
	default:
		return 0, 0, nil
	}
	if err := row.Scan(&tokens, &cost); err != nil {
		return 0, 0, err
	}
	return tokens, cost, nil
}

// activeReservations sums reserved (not yet reconciled/released/expired) tokens and cost.
// When userID is empty, sums across the whole organization (org-level scopes).
func (b *BudgetStore) activeReservations(ctx context.Context, q rowScanner, orgID, userID string) (int64, float64, error) {
	var tokens int64
	var cost float64
	var row pgx.Row
	if userID == "" {
		row = q.QueryRow(ctx, `
			SELECT COALESCE(SUM(reserved_tokens), 0), COALESCE(SUM(reserved_cost_usd), 0)
			FROM app.llm_budget_reservations
			WHERE organization_id = $1::uuid AND status = 'reserved' AND expires_at > NOW()
		`, orgID)
	} else {
		row = q.QueryRow(ctx, `
			SELECT COALESCE(SUM(reserved_tokens), 0), COALESCE(SUM(reserved_cost_usd), 0)
			FROM app.llm_budget_reservations
			WHERE organization_id = $1::uuid AND user_id = $2 AND status = 'reserved' AND expires_at > NOW()
		`, orgID, userID)
	}
	if err := row.Scan(&tokens, &cost); err != nil {
		return 0, 0, err
	}
	return tokens, cost, nil
}

// ledgerError maps a ledger read/write error to either a hard denial
// (FailClosed) or nil (legacy fail-open advisory behavior), and records
// observability signal either way.
func (b *BudgetStore) ledgerError(err error) error {
	if err == nil {
		return nil
	}
	observability.IncAuditWriteFailure()
	if b.cfg.FailClosed {
		observability.IncLLMBudgetFailClosed()
		return fmt.Errorf("%w: %v", ErrBudgetLedgerUnavailable, err)
	}
	return nil
}

// Reserve atomically checks org/user budgets (committed usage + active
// reservations) and, if within limits, inserts a reservation row for
// estimatedTokens (prompt estimate + configured max output allowance) before
// the provider call is made. The check-and-insert is serialized per
// org+user pair with a Postgres advisory lock so concurrent requests near
// the limit cannot both pass the check.
//
// Returns an empty requestID with a nil error when budgets are disabled
// (nothing to reserve). On denial, returns an error describing which
// budget would be exceeded. On ledger error, denies only when FailClosed
// is configured; otherwise fails open and returns ("", nil).
func (b *BudgetStore) Reserve(ctx context.Context, orgID, userID string, estimatedTokens int) (string, error) {
	if b == nil || b.pool == nil || !b.Enabled() {
		return "", nil
	}
	orgID = normalizeOrgID(orgID)
	userID = normalizeUserID(userID)
	if estimatedTokens < 0 {
		estimatedTokens = 0
	}
	estimatedCost := b.EstimateCostUSD(estimatedTokens)

	conn, err := b.pool.Acquire(ctx)
	if err != nil {
		return "", b.ledgerError(err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT set_config('app.current_org_id', $1, false)`, orgID); err != nil {
		return "", b.ledgerError(err)
	}
	defer func() {
		_, _ = conn.Exec(context.Background(), `SELECT set_config('app.current_org_id', '', false)`)
	}()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return "", b.ledgerError(err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.Background())
		}
	}()

	// Serialize the check-then-insert for this org+user pair so two
	// concurrent requests near the limit cannot both observe "under limit".
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, orgID+":"+userID); err != nil {
		return "", b.ledgerError(err)
	}

	if err := b.checkAllScopes(ctx, tx, orgID, userID, estimatedTokens, estimatedCost); err != nil {
		return "", err
	}

	ttl := b.cfg.ReservationTTL
	if ttl <= 0 {
		ttl = defaultReservationTTL
	}
	var requestID string
	err = tx.QueryRow(ctx, `
		INSERT INTO app.llm_budget_reservations (organization_id, user_id, reserved_tokens, reserved_cost_usd, status, expires_at)
		VALUES ($1::uuid, $2, $3, $4, 'reserved', NOW() + $5::interval)
		RETURNING request_id::text
	`, orgID, userID, estimatedTokens, estimatedCost, fmt.Sprintf("%d seconds", int(ttl.Seconds()))).Scan(&requestID)
	if err != nil {
		return "", b.ledgerError(err)
	}

	if err := tx.Commit(ctx); err != nil {
		return "", b.ledgerError(err)
	}
	committed = true
	return requestID, nil
}

// ReconcileUsage marks a reservation committed with actual token/cost usage
// and folds the actual usage into the daily/monthly ledgers (RecordUsage).
// Call after a successful provider response. No-op when requestID is empty
// (budgets disabled or Reserve was skipped).
func (b *BudgetStore) ReconcileUsage(ctx context.Context, requestID, orgID, userID string, promptTokens, completionTokens int) {
	if b == nil || b.pool == nil {
		return
	}
	orgID = normalizeOrgID(orgID)
	userID = normalizeUserID(userID)
	actualCost := b.EstimateCostUSD(promptTokens + completionTokens)
	if requestID != "" {
		ctx2, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := db.ExecWithOrg(ctx2, b.pool, orgID, `
			UPDATE app.llm_budget_reservations
			SET status = 'committed', reserved_tokens = $2, reserved_cost_usd = $3, updated_at = NOW()
			WHERE request_id = $1::uuid AND status = 'reserved'
		`, requestID, promptTokens+completionTokens, actualCost); err != nil {
			observability.IncAuditWriteFailure()
		}
	}
	b.RecordUsage(ctx, orgID, userID, promptTokens, completionTokens)
}

// ReleaseReservation releases a reservation without recording usage, e.g.
// when the provider call failed or was denied after the reservation was made.
// No-op when requestID is empty.
func (b *BudgetStore) ReleaseReservation(ctx context.Context, requestID, orgID string) {
	if b == nil || b.pool == nil || requestID == "" {
		return
	}
	orgID = normalizeOrgID(orgID)
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := db.ExecWithOrg(ctx, b.pool, orgID, `
		UPDATE app.llm_budget_reservations
		SET status = 'released', updated_at = NOW()
		WHERE request_id = $1::uuid AND status = 'reserved'
	`, requestID); err != nil {
		observability.IncAuditWriteFailure()
	}
}

// ExpireAbandoned marks reservations past their expiry as expired so they
// stop counting against budgets (e.g. the process crashed between Reserve
// and ReconcileUsage/ReleaseReservation). Intended to run periodically from
// a maintenance job. Returns the number of reservations expired.
func (b *BudgetStore) ExpireAbandoned(ctx context.Context) (int64, error) {
	if b == nil || b.pool == nil {
		return 0, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tag, err := b.pool.Exec(ctx, `
		UPDATE app.llm_budget_reservations
		SET status = 'expired', updated_at = NOW()
		WHERE status = 'reserved' AND expires_at < NOW()
	`)
	if err != nil {
		return 0, err
	}
	n := tag.RowsAffected()
	observability.IncLLMBudgetReservationExpired(n)
	return n, nil
}

// StartReservationCleanupLoop periodically expires abandoned budget reservations.
// interval <= 0 disables the loop.
func StartReservationCleanupLoop(ctx context.Context, store *BudgetStore, interval time.Duration) {
	if store == nil || interval <= 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_, _ = store.ExpireAbandoned(ctx)
			}
		}
	}()
}

// RecordUsage increments org and user budget ledgers.
func (b *BudgetStore) RecordUsage(ctx context.Context, orgID, userID string, promptTokens, completionTokens int) {
	if b == nil || b.pool == nil {
		return
	}
	orgID = normalizeOrgID(orgID)
	userID = normalizeUserID(userID)
	cost := b.EstimateCostUSD(promptTokens + completionTokens)
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := db.ExecWithOrg(ctx, b.pool, orgID, `
		INSERT INTO app.llm_budget_usage (
			organization_id, usage_date, prompt_tokens, completion_tokens, estimated_cost_usd, call_count
		) VALUES ($1::uuid, CURRENT_DATE, $2, $3, $4, 1)
		ON CONFLICT (organization_id, usage_date) DO UPDATE SET
			prompt_tokens = app.llm_budget_usage.prompt_tokens + EXCLUDED.prompt_tokens,
			completion_tokens = app.llm_budget_usage.completion_tokens + EXCLUDED.completion_tokens,
			estimated_cost_usd = app.llm_budget_usage.estimated_cost_usd + EXCLUDED.estimated_cost_usd,
			call_count = app.llm_budget_usage.call_count + 1
	`, orgID, promptTokens, completionTokens, cost); err != nil {
		observability.IncAuditWriteFailure()
	}
	if userID == "" {
		return
	}
	if err := db.ExecWithOrg(ctx, b.pool, orgID, `
		INSERT INTO app.llm_user_budget_usage (
			organization_id, user_id, usage_date, prompt_tokens, completion_tokens, estimated_cost_usd, call_count
		) VALUES ($1::uuid, $2, CURRENT_DATE, $3, $4, $5, 1)
		ON CONFLICT (organization_id, user_id, usage_date) DO UPDATE SET
			prompt_tokens = app.llm_user_budget_usage.prompt_tokens + EXCLUDED.prompt_tokens,
			completion_tokens = app.llm_user_budget_usage.completion_tokens + EXCLUDED.completion_tokens,
			estimated_cost_usd = app.llm_user_budget_usage.estimated_cost_usd + EXCLUDED.estimated_cost_usd,
			call_count = app.llm_user_budget_usage.call_count + 1
	`, orgID, userID, promptTokens, completionTokens, cost); err != nil {
		observability.IncAuditWriteFailure()
	}
}

// EstimateCostUSD returns the estimated USD cost for a token count.
func (b *BudgetStore) EstimateCostUSD(tokens int) float64 {
	if b == nil {
		return 0
	}
	rate := b.cfg.USDPer1kTokens
	if rate <= 0 {
		rate = 0.002
	}
	return float64(tokens) / 1000.0 * rate
}

func normalizeOrgID(orgID string) string {
	if orgID == "" {
		return auth.DefaultOrgID()
	}
	return orgID
}

func normalizeUserID(userID string) string {
	return userID
}
