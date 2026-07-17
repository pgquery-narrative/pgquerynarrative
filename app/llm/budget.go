package llm

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/app/db"
	"github.com/pgquerynarrative/pgquerynarrative/app/observability"
)

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

// Check returns an error when org or user budgets would be exceeded.
func (b *BudgetStore) Check(ctx context.Context, orgID, userID string, upcomingTokens int) error {
	if !b.Enabled() {
		return nil
	}
	orgID = normalizeOrgID(orgID)
	userID = normalizeUserID(userID)
	upcomingCost := b.EstimateCostUSD(upcomingTokens)

	if err := b.checkOrgDaily(ctx, orgID, upcomingTokens, upcomingCost); err != nil {
		return err
	}
	if err := b.checkOrgMonthly(ctx, orgID, upcomingTokens, upcomingCost); err != nil {
		return err
	}
	if userID != "" {
		if err := b.checkUserDaily(ctx, orgID, userID, upcomingTokens, upcomingCost); err != nil {
			return err
		}
		if err := b.checkUserMonthly(ctx, orgID, userID, upcomingTokens, upcomingCost); err != nil {
			return err
		}
	}
	return nil
}

func (b *BudgetStore) checkOrgDaily(ctx context.Context, orgID string, upcomingTokens int, upcomingCost float64) error {
	if b.cfg.DailyTokenLimit <= 0 && b.cfg.DailyCostUSD <= 0 {
		return nil
	}
	tokens, cost := b.queryOrgUsage(ctx, orgID, `
		SELECT COALESCE(prompt_tokens + completion_tokens, 0), COALESCE(estimated_cost_usd, 0)
		FROM app.llm_budget_usage
		WHERE organization_id = $1::uuid AND usage_date = CURRENT_DATE
	`, orgID)
	if b.cfg.DailyTokenLimit > 0 && int(tokens)+upcomingTokens > b.cfg.DailyTokenLimit {
		return fmt.Errorf("LLM daily token budget exceeded for organization (%d/%d)", tokens, b.cfg.DailyTokenLimit)
	}
	if b.cfg.DailyCostUSD > 0 && cost+upcomingCost > b.cfg.DailyCostUSD {
		return fmt.Errorf("LLM daily cost budget exceeded for organization (%.4f/%.4f USD)", cost, b.cfg.DailyCostUSD)
	}
	return nil
}

func (b *BudgetStore) checkOrgMonthly(ctx context.Context, orgID string, upcomingTokens int, upcomingCost float64) error {
	if b.cfg.MonthlyTokenLimit <= 0 && b.cfg.MonthlyCostUSD <= 0 {
		return nil
	}
	tokens, cost := b.queryOrgUsage(ctx, orgID, `
		SELECT COALESCE(SUM(prompt_tokens + completion_tokens), 0), COALESCE(SUM(estimated_cost_usd), 0)
		FROM app.llm_budget_usage
		WHERE organization_id = $1::uuid
		  AND usage_date >= date_trunc('month', CURRENT_DATE)::date
	`, orgID)
	if b.cfg.MonthlyTokenLimit > 0 && int(tokens)+upcomingTokens > b.cfg.MonthlyTokenLimit {
		return fmt.Errorf("LLM monthly token budget exceeded for organization (%d/%d)", tokens, b.cfg.MonthlyTokenLimit)
	}
	if b.cfg.MonthlyCostUSD > 0 && cost+upcomingCost > b.cfg.MonthlyCostUSD {
		return fmt.Errorf("LLM monthly cost budget exceeded for organization (%.4f/%.4f USD)", cost, b.cfg.MonthlyCostUSD)
	}
	return nil
}

func (b *BudgetStore) checkUserDaily(ctx context.Context, orgID, userID string, upcomingTokens int, upcomingCost float64) error {
	if b.cfg.PerUserDailyTokenLimit <= 0 && b.cfg.PerUserDailyCostUSD <= 0 {
		return nil
	}
	tokens, cost := b.queryUserUsage(ctx, orgID, userID, `
		SELECT COALESCE(prompt_tokens + completion_tokens, 0), COALESCE(estimated_cost_usd, 0)
		FROM app.llm_user_budget_usage
		WHERE organization_id = $1::uuid AND user_id = $2 AND usage_date = CURRENT_DATE
	`, orgID, userID)
	if b.cfg.PerUserDailyTokenLimit > 0 && int(tokens)+upcomingTokens > b.cfg.PerUserDailyTokenLimit {
		return fmt.Errorf("LLM daily token budget exceeded for user %q (%d/%d)", userID, tokens, b.cfg.PerUserDailyTokenLimit)
	}
	if b.cfg.PerUserDailyCostUSD > 0 && cost+upcomingCost > b.cfg.PerUserDailyCostUSD {
		return fmt.Errorf("LLM daily cost budget exceeded for user %q (%.4f/%.4f USD)", userID, cost, b.cfg.PerUserDailyCostUSD)
	}
	return nil
}

func (b *BudgetStore) checkUserMonthly(ctx context.Context, orgID, userID string, upcomingTokens int, upcomingCost float64) error {
	if b.cfg.PerUserMonthlyTokenLimit <= 0 && b.cfg.PerUserMonthlyCostUSD <= 0 {
		return nil
	}
	tokens, cost := b.queryUserUsage(ctx, orgID, userID, `
		SELECT COALESCE(SUM(prompt_tokens + completion_tokens), 0), COALESCE(SUM(estimated_cost_usd), 0)
		FROM app.llm_user_budget_usage
		WHERE organization_id = $1::uuid AND user_id = $2
		  AND usage_date >= date_trunc('month', CURRENT_DATE)::date
	`, orgID, userID)
	if b.cfg.PerUserMonthlyTokenLimit > 0 && int(tokens)+upcomingTokens > b.cfg.PerUserMonthlyTokenLimit {
		return fmt.Errorf("LLM monthly token budget exceeded for user %q (%d/%d)", userID, tokens, b.cfg.PerUserMonthlyTokenLimit)
	}
	if b.cfg.PerUserMonthlyCostUSD > 0 && cost+upcomingCost > b.cfg.PerUserMonthlyCostUSD {
		return fmt.Errorf("LLM monthly cost budget exceeded for user %q (%.4f/%.4f USD)", userID, cost, b.cfg.PerUserMonthlyCostUSD)
	}
	return nil
}

func (b *BudgetStore) queryOrgUsage(ctx context.Context, orgID, query string, args ...interface{}) (int64, float64) {
	var tokens int64
	var cost float64
	scan := db.QueryRowWithOrg(ctx, b.pool, orgID, query, args...)
	if err := scan(&tokens, &cost); err != nil {
		return 0, 0
	}
	return tokens, cost
}

func (b *BudgetStore) queryUserUsage(ctx context.Context, orgID, userID, query string, args ...interface{}) (int64, float64) {
	var tokens int64
	var cost float64
	scan := db.QueryRowWithOrg(ctx, b.pool, orgID, query, args...)
	if err := scan(&tokens, &cost); err != nil {
		return 0, 0
	}
	return tokens, cost
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
