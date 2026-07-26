package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	queriesapi "github.com/pgquerynarrative/pgquerynarrative/api/gen/queries"
	reportsapi "github.com/pgquerynarrative/pgquerynarrative/api/gen/reports"
	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/app/db"
	"github.com/pgquerynarrative/pgquerynarrative/app/security"
	"github.com/pgquerynarrative/pgquerynarrative/gen/schedules"
)

type SchedulesService struct {
	appPool       db.DB
	rawPool       *pgxpool.Pool
	queriesSvc    *QueriesService
	reportsSvc    *ReportsService
	webhookClient *security.WebhookClient
	webhookSecret string
	allowedHosts  []string
	authz         ConnectionAuthorizer
}

func NewSchedulesService(appPool db.DB, queriesSvc *QueriesService, reportsSvc *ReportsService) *SchedulesService {
	return &SchedulesService{
		appPool:    appPool,
		queriesSvc: queriesSvc,
		reportsSvc: reportsSvc,
	}
}

// SetAuthorizer wires connection-level authorization. Nil is permissive.
// Intended to be called only once, from narrative.NewClient, before the
// service is handed to any HTTP handler or background worker.
func (s *SchedulesService) SetAuthorizer(authz ConnectionAuthorizer) {
	if s != nil {
		s.authz = authz
	}
}

// SetRawPool sets the underlying pool for cross-org scheduler claims and retries.
func (s *SchedulesService) SetRawPool(pool *pgxpool.Pool) {
	if s != nil {
		s.rawPool = pool
	}
}

// ConfigureWebhook sets the signing secret and hostname allowlist immutably for this service.
// allowedHosts is copied so later caller mutation cannot change runtime policy.
// Intended to be called exactly once, from narrative.NewClient, before this
// service is handed to any HTTP handler or background worker (e.g.
// service.StartWebhookRetryWorker); do not call again to "update" policy at
// runtime.
func (s *SchedulesService) ConfigureWebhook(secret string, allowedHosts []string) {
	if s == nil {
		return
	}
	s.webhookSecret = strings.TrimSpace(secret)
	s.allowedHosts = append([]string(nil), allowedHosts...)
	s.webhookClient = security.NewWebhookClient(s.webhookSecret, 10*time.Second, s.allowedHosts...)
}

// WebhookAllowedHosts returns a copy of the active webhook hostname allowlist (no secrets).
func (s *SchedulesService) WebhookAllowedHosts() []string {
	if s == nil {
		return nil
	}
	return append([]string(nil), s.allowedHosts...)
}

func (s *SchedulesService) webhookAllowedHosts() []string {
	return s.WebhookAllowedHosts()
}

func (s *SchedulesService) List(ctx context.Context) (*schedules.ScheduleListResult, error) {
	rows, err := s.appPool.Query(ctx, `
		SELECT id, name, saved_query_id, sql, connection_id, interval_expr, destination_type, destination_target, enabled,
		       last_run_at, last_status, last_error, next_run_at, created_at, updated_at
		FROM app.schedules
		WHERE organization_id = $1
		ORDER BY created_at DESC
	`, orgID(ctx))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]*schedules.Schedule, 0)
	for rows.Next() {
		item, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		s.decryptScheduleSQL(item)
		items = append(items, item)
	}
	return &schedules.ScheduleListResult{Items: items}, rows.Err()
}

func (s *SchedulesService) Create(ctx context.Context, payload *schedules.ScheduleInput) (*schedules.Schedule, error) {
	if err := validateScheduleInput(payload, s.webhookAllowedHosts()); err != nil {
		return nil, scheduleValidationError(err)
	}
	connID, err := s.queriesSvc.resolveConnectionID(payload.ConnectionID)
	if err != nil {
		return nil, scheduleConnectionNotFoundError(err)
	}
	if err := checkConnectionAccess(ctx, s.authz, connID, auth.ActionSchedule); err != nil {
		return nil, scheduleConnectionForbiddenError(err)
	}
	if err := s.validateScheduleSQL(ctx, payload); err != nil {
		return nil, scheduleValidationError(err)
	}
	nextRunAt, err := computeNextRun(payload.IntervalExpr, time.Now().UTC())
	if err != nil {
		return nil, scheduleValidationError(err)
	}
	var id string
	p := auth.PrincipalFromContext(ctx)
	sqlAtRest, sealErr := sealProductSQL(s.productEncKey(), strings.TrimSpace(ptrString(payload.SQL)))
	if sealErr != nil {
		return nil, &schedules.ValidationError{Name: "validation_error", Message: "failed to protect schedule SQL at rest", Code: strPtr("ENCRYPTION_ERROR")}
	}
	err = s.appPool.QueryRow(ctx, `
		INSERT INTO app.schedules (name, saved_query_id, sql, connection_id, interval_expr, destination_type, destination_target, enabled, next_run_at, organization_id, created_by)
		VALUES ($1, $2, NULLIF($3, ''), COALESCE(NULLIF($4, ''), 'default'), $5, $6, $7, COALESCE($8, true), $9, $10, $11)
		RETURNING id
	`, payload.Name, payload.SavedQueryID, sqlAtRest, payload.ConnectionID, payload.IntervalExpr, payload.DestinationType, payload.DestinationTarget, payload.Enabled, nextRunAt, p.OrgID, p.UserID).Scan(&id)
	if err != nil {
		return nil, scheduleStoreError(err)
	}
	return s.getByID(ctx, id)
}

func (s *SchedulesService) Update(ctx context.Context, payload *schedules.UpdatePayload) (*schedules.Schedule, error) {
	current, err := s.getByID(ctx, payload.ID)
	if err != nil {
		return nil, err
	}
	in := &schedules.ScheduleInput{
		Name:              firstNonBlank(payload.Name, current.Name),
		SavedQueryID:      coalesceStrPtr(payload.SavedQueryID, current.SavedQueryID),
		SQL:               coalesceStrPtr(payload.SQL, current.SQL),
		ConnectionID:      coalescePtrOrBlank(payload.ConnectionID, current.ConnectionID),
		IntervalExpr:      firstNonBlank(payload.IntervalExpr, current.IntervalExpr),
		DestinationType:   firstNonBlank(payload.DestinationType, current.DestinationType),
		DestinationTarget: strPtrIfNotEmpty(firstNonBlank(ptrString(payload.DestinationTarget), ptrString(current.DestinationTarget))),
		Enabled:           coalesceBoolPtr(payload.Enabled, current.Enabled),
	}
	if err := validateScheduleInput(in, s.webhookAllowedHosts()); err != nil {
		return nil, scheduleValidationError(err)
	}
	if err := s.validateScheduleSQL(ctx, in); err != nil {
		return nil, scheduleValidationError(err)
	}
	nextRunAt, err := computeNextRun(in.IntervalExpr, time.Now().UTC())
	if err != nil {
		return nil, scheduleValidationError(err)
	}
	sqlAtRest, sealErr := sealProductSQL(s.productEncKey(), ptrString(in.SQL))
	if sealErr != nil {
		return nil, &schedules.ValidationError{Name: "validation_error", Message: "failed to protect schedule SQL at rest", Code: strPtr("ENCRYPTION_ERROR")}
	}
	_, err = s.appPool.Exec(ctx, `
		UPDATE app.schedules
		SET name = $2, saved_query_id = $3, sql = NULLIF($4, ''), connection_id = COALESCE(NULLIF($5, ''), 'default'),
		    interval_expr = $6, destination_type = $7, destination_target = $8, enabled = COALESCE($9, enabled),
		    next_run_at = $10, updated_at = NOW()
		WHERE id = $1 AND organization_id = $11
	`, payload.ID, in.Name, in.SavedQueryID, sqlAtRest, in.ConnectionID, in.IntervalExpr, in.DestinationType, in.DestinationTarget, in.Enabled, nextRunAt, orgID(ctx))
	if err != nil {
		return nil, scheduleStoreError(err)
	}
	return s.getByID(ctx, payload.ID)
}

func (s *SchedulesService) Delete(ctx context.Context, payload *schedules.DeletePayload) error {
	tag, err := s.appPool.Exec(ctx, `DELETE FROM app.schedules WHERE id = $1 AND organization_id = $2`, payload.ID, orgID(ctx))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return &schedules.NotFoundError{Name: "not_found", Message: "schedule not found", Code: strPtr("NOT_FOUND")}
	}
	return nil
}

func (s *SchedulesService) RunNow(ctx context.Context, payload *schedules.RunNowPayload) (*schedules.ScheduleRunResult, error) {
	sc, err := s.getByID(ctx, payload.ID)
	if err != nil {
		return nil, err
	}
	workerID := scheduleWorkerID()
	now := time.Now().UTC()
	// Request-scoped idempotency: same second coalesces; unique per manual trigger otherwise.
	idempotencyKey := fmt.Sprintf("manual:%s:%d", sc.ID, now.UnixNano())
	leaseUntil := now.Add(defaultScheduleLease)
	var runID string
	err = s.appPool.QueryRow(ctx, `
		INSERT INTO app.schedule_runs (
			schedule_id, organization_id, scheduled_for, idempotency_key,
			worker_id, lease_until, status, attempt_count, started_at
		) VALUES ($1,$2,$3,$4,$5,$6,'running',1,NOW())
		RETURNING id
	`, sc.ID, orgID(ctx), now, idempotencyKey, workerID, leaseUntil).Scan(&runID)
	if err != nil {
		return nil, err
	}
	_, _ = s.appPool.Exec(ctx, `
		UPDATE app.schedules
		SET locked_by = $2, locked_until = $3, updated_at = NOW()
		WHERE id = $1 AND organization_id = $4
	`, sc.ID, workerID, leaseUntil, orgID(ctx))

	claim := claimedScheduleRun{
		RunID:        runID,
		ScheduleID:   sc.ID,
		OrgID:        orgID(ctx),
		ScheduledFor: now,
	}
	runErr := s.executeClaimedRun(ctx, workerID, claim)
	updated, _ := s.getByID(ctx, sc.ID)
	var reportID *string
	var runStatus string
	row := s.appPool.QueryRow(ctx, `SELECT report_id::text, status FROM app.schedule_runs WHERE id = $1`, runID)
	var rid *string
	if scanErr := row.Scan(&rid, &runStatus); scanErr == nil && rid != nil && *rid != "" {
		reportID = rid
	}
	// Delivered reflects true terminal success (log destinations, or a webhook that resolved
	// synchronously to delivered). A webhook still "pending" in the outbox is not yet
	// delivered even though the run itself did not error.
	delivered := runStatus == "completed"
	return &schedules.ScheduleRunResult{Schedule: updated, ReportID: reportID, Delivered: delivered}, runErr
}

// runSchedule generates (or reuses) the report for a durable schedule run and routes it to
// its destination. scheduleRunID must be set by every real caller (executeClaimedRun always
// has one); it is what makes both report generation and webhook delivery idempotent across
// worker retries and crash recovery.
func (s *SchedulesService) runSchedule(ctx context.Context, sc *schedules.Schedule, scheduleRunID string) (reportID, deliveryStatus string, err error) {
	sqlText := strings.TrimSpace(ptrString(sc.SQL))
	if sc.SavedQueryID != nil && *sc.SavedQueryID != "" {
		sq, err := s.queriesSvc.GetSaved(ctx, &queriesapi.GetSavedPayload{ID: *sc.SavedQueryID})
		if err != nil {
			return "", "", err
		}
		sqlText = sq.SQL
	}
	if sqlText == "" {
		return "", "", errors.New("schedule has neither SQL nor valid saved query")
	}
	reportID, err = s.reportsSvc.GenerateForScheduleRun(ctx, &reportsapi.GenerateReportPayload{SQL: sqlText, ConnectionID: strPtrIfNotEmpty(sc.ConnectionID)}, scheduleRunID)
	if err != nil {
		return "", "", err
	}
	if scheduleRunID != "" {
		// Best-effort visibility only: app.reports.schedule_run_id (set by storeReport) is
		// the source of truth used for report reuse; this just surfaces it on the run row
		// promptly instead of waiting for finalization.
		_, _ = s.appPool.Exec(ctx, `
			UPDATE app.schedule_runs SET report_id = $2 WHERE id = $1 AND report_id IS NULL
		`, scheduleRunID, reportID)
	}
	deliveryStatus, err = s.deliverReport(ctx, sc, scheduleRunID, reportID)
	if err != nil {
		return reportID, "", err
	}
	return reportID, deliveryStatus, nil
}

// deliverReport routes a generated report to its destination. Log destinations complete
// immediately. Webhook destinations use the transactional outbox: a durable delivery row
// (stable delivery ID keyed by schedule_run_id) is inserted BEFORE any network I/O, then one
// best-effort synchronous attempt is made so the common case still delivers with low latency.
// The returned status is one of "completed" (log), "delivered", "dead_letter", or "pending"
// (durably enqueued but not yet resolved — the background outbox worker will retry it).
func (s *SchedulesService) deliverReport(ctx context.Context, sc *schedules.Schedule, scheduleRunID, reportID string) (string, error) {
	switch strings.ToLower(sc.DestinationType) {
	case "log":
		return "completed", nil
	case "webhook":
		target := strings.TrimSpace(ptrString(sc.DestinationTarget))
		if target == "" {
			return "", errors.New("webhook target is required")
		}
		deliveryID, err := s.enqueueWebhookDelivery(ctx, sc, scheduleRunID, reportID)
		if err != nil {
			return "", fmt.Errorf("enqueue webhook delivery: %w", err)
		}
		status, attemptErr := s.attemptOutboxDelivery(ctx, deliveryID)
		if attemptErr != nil {
			// The delivery row is durably enqueued; a transient error while attempting it
			// now (e.g. a DB hiccup mid-update) is not fatal to the schedule run — the
			// background outbox worker (RetryFailedWebhooks) will pick it up and retry.
			return "pending", nil
		}
		return status, nil
	default:
		return "", errors.New("unsupported destination_type")
	}
}

// enqueueWebhookDelivery inserts (or reuses) the outbox row for this schedule run's webhook
// delivery. The delivery/idempotency ID is derived from scheduleRunID alone — never from
// report_id — so a report regenerated during crash recovery still reuses the exact same
// delivery ID and the receiver-side X-PGQN-Delivery-ID dedup keeps working.
func (s *SchedulesService) enqueueWebhookDelivery(ctx context.Context, sc *schedules.Schedule, scheduleRunID, reportID string) (string, error) {
	idempotencyKey, err := webhookDeliveryIdempotencyKey(scheduleRunID)
	if err != nil {
		return "", err
	}
	target := strings.TrimSpace(ptrString(sc.DestinationTarget))
	payload := map[string]any{
		"schedule_id":     sc.ID,
		"schedule_run_id": scheduleRunID,
		"report_id":       reportID,
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	var deliveryID string
	err = s.appPool.QueryRow(ctx, `
		INSERT INTO app.webhook_deliveries (
			organization_id, schedule_id, schedule_run_id, destination_url, idempotency_key,
			payload, status, next_attempt_at
		) VALUES ($1, $2, NULLIF($3, '')::uuid, $4, $5, $6, 'pending', NOW())
		ON CONFLICT (idempotency_key) DO UPDATE SET
			destination_url = EXCLUDED.destination_url,
			payload = EXCLUDED.payload
		WHERE app.webhook_deliveries.status = 'pending'
		RETURNING id
	`, orgID(ctx), sc.ID, scheduleRunID, target, idempotencyKey, payloadJSON).Scan(&deliveryID)
	if errors.Is(err, pgx.ErrNoRows) {
		// Row exists but is already delivering/delivered/dead_letter (concurrent
		// worker/retry); fetch its id so the caller can inspect current status.
		err = s.appPool.QueryRow(ctx, `
			SELECT id FROM app.webhook_deliveries WHERE idempotency_key = $1
		`, idempotencyKey).Scan(&deliveryID)
	}
	if err != nil {
		return "", err
	}
	return deliveryID, nil
}

// webhookDeliveryIdempotencyKey computes the stable delivery ID for a schedule run's webhook
// destination. A schedule has exactly one destination today, so the run id alone uniquely
// identifies the (schedule_run_id, destination) pair; the id is reused verbatim as the
// X-PGQN-Delivery-ID header on every attempt, including after report regeneration.
func webhookDeliveryIdempotencyKey(scheduleRunID string) (string, error) {
	scheduleRunID = strings.TrimSpace(scheduleRunID)
	if scheduleRunID == "" {
		return "", errors.New("schedule_run_id is required for durable webhook delivery")
	}
	return "schedule-run:" + scheduleRunID, nil
}

func (s *SchedulesService) getByID(ctx context.Context, id string) (*schedules.Schedule, error) {
	return s.getByIDForOrg(ctx, id, orgID(ctx))
}

func (s *SchedulesService) getByIDForOrg(ctx context.Context, id, organizationID string) (*schedules.Schedule, error) {
	row := s.appPool.QueryRow(ctx, `
		SELECT id, name, saved_query_id, sql, connection_id, interval_expr, destination_type, destination_target, enabled,
		       last_run_at, last_status, last_error, next_run_at, created_at, updated_at
		FROM app.schedules
		WHERE id = $1 AND organization_id = $2
	`, id, organizationID)
	item, err := scanSchedule(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, &schedules.NotFoundError{Name: "not_found", Message: "schedule not found", Code: strPtr("NOT_FOUND")}
		}
		return nil, err
	}
	s.decryptScheduleSQL(item)
	return item, nil
}

func (s *SchedulesService) productEncKey() []byte {
	if s != nil && s.queriesSvc != nil {
		return s.queriesSvc.dataEncKey
	}
	return nil
}

func (s *SchedulesService) decryptScheduleSQL(sc *schedules.Schedule) {
	if sc == nil || sc.SQL == nil {
		return
	}
	opened := openProductSQL(s.productEncKey(), *sc.SQL)
	sc.SQL = &opened
}

type scanner interface{ Scan(dest ...any) error }

func scanSchedule(r scanner) (*schedules.Schedule, error) {
	var s schedules.Schedule
	var savedID sql.NullString
	var sqlText sql.NullString
	var destTarget sql.NullString
	var lastRun sql.NullTime
	var lastStatus sql.NullString
	var lastError sql.NullString
	var nextRun sql.NullTime
	var createdAt time.Time
	var updatedAt time.Time
	if err := r.Scan(&s.ID, &s.Name, &savedID, &sqlText, &s.ConnectionID, &s.IntervalExpr, &s.DestinationType, &destTarget, &s.Enabled,
		&lastRun, &lastStatus, &lastError, &nextRun, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	if savedID.Valid {
		s.SavedQueryID = &savedID.String
	}
	if sqlText.Valid {
		s.SQL = &sqlText.String
	}
	if destTarget.Valid {
		s.DestinationTarget = &destTarget.String
	} else {
		empty := ""
		s.DestinationTarget = &empty
	}
	if lastRun.Valid {
		v := lastRun.Time.UTC().Format(time.RFC3339)
		s.LastRunAt = &v
	}
	if lastStatus.Valid {
		s.LastStatus = &lastStatus.String
	}
	if lastError.Valid {
		s.LastError = &lastError.String
	}
	if nextRun.Valid {
		v := nextRun.Time.UTC().Format(time.RFC3339)
		s.NextRunAt = &v
	}
	s.CreatedAt = createdAt.UTC().Format(time.RFC3339)
	s.UpdatedAt = updatedAt.UTC().Format(time.RFC3339)
	return &s, nil
}

func validateScheduleInput(in *schedules.ScheduleInput, allowedHosts []string) error {
	if strings.TrimSpace(in.Name) == "" {
		return errors.New("name is required")
	}
	if strings.TrimSpace(in.IntervalExpr) == "" {
		return errors.New("interval_expr is required")
	}
	if _, err := computeNextRun(in.IntervalExpr, time.Now().UTC()); err != nil {
		return err
	}
	dt := strings.ToLower(strings.TrimSpace(in.DestinationType))
	if dt != "webhook" && dt != "log" {
		return errors.New("destination_type must be webhook or log")
	}
	target := strings.TrimSpace(ptrString(in.DestinationTarget))
	if dt == "webhook" {
		if target == "" {
			return errors.New("destination_target is required for webhook destinations")
		}
		if err := security.ValidateWebhookURL(target); err != nil {
			return err
		}
		if err := security.ValidateWebhookHostAllowlist(target, allowedHosts); err != nil {
			return err
		}
	}
	// log destinations do not require a target
	return nil
}

func (s *SchedulesService) validateScheduleSQL(ctx context.Context, in *schedules.ScheduleInput) error {
	sqlText := strings.TrimSpace(ptrString(in.SQL))
	if in.SavedQueryID != nil && strings.TrimSpace(*in.SavedQueryID) != "" {
		sq, err := s.queriesSvc.GetSaved(ctx, &queriesapi.GetSavedPayload{ID: *in.SavedQueryID})
		if err != nil {
			return errors.New("saved_query_id is invalid")
		}
		sqlText = sq.SQL
	}
	if sqlText == "" {
		return errors.New("sql or saved_query_id is required")
	}
	if err := s.queriesSvc.ValidateQuery(in.ConnectionID, sqlText); err != nil {
		return err
	}
	return nil
}

func computeNextRun(expr string, from time.Time) (time.Time, error) {
	expr = strings.TrimSpace(expr)
	const prefix = "@every "
	if !strings.HasPrefix(expr, prefix) {
		return time.Time{}, errors.New("interval_expr must use '@every <duration>' format")
	}
	d, err := time.ParseDuration(strings.TrimSpace(strings.TrimPrefix(expr, prefix)))
	if err != nil || d <= 0 {
		return time.Time{}, errors.New("invalid @every duration")
	}
	return from.Add(d), nil
}

func firstNonBlank(v string, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return strings.TrimSpace(v)
}

func coalescePtrOrBlank(v *string, fallback string) *string {
	if v != nil {
		if strings.TrimSpace(*v) == "" {
			return nil
		}
		return v
	}
	if strings.TrimSpace(fallback) == "" {
		return nil
	}
	return &fallback
}

func coalesceStrPtr(v *string, fallback *string) *string {
	if v != nil {
		return v
	}
	return fallback
}

func coalesceBoolPtr(v *bool, fallback bool) *bool {
	if v != nil {
		return v
	}
	return &fallback
}

func nullInt(v int) any {
	if v == 0 {
		return nil
	}
	return v
}

func ptrString(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func scheduleValidationError(err error) error {
	if err == nil {
		return nil
	}
	return &schedules.ValidationError{Name: "validation_error", Message: SanitizeAPIError(err, "Invalid schedule."), Code: strPtr("VALIDATION_ERROR")}
}

func scheduleConnectionNotFoundError(err error) error {
	if err == nil {
		return nil
	}
	return &schedules.ValidationError{Name: "validation_error", Message: "connection not found", Code: strPtr("CONNECTION_NOT_FOUND")}
}

func scheduleConnectionForbiddenError(err error) error {
	if err == nil {
		return nil
	}
	return &schedules.ValidationError{Name: "validation_error", Message: "connection access denied", Code: strPtr("CONNECTION_FORBIDDEN")}
}

func scheduleStoreError(err error) error {
	if err == nil {
		return nil
	}
	return &schedules.ValidationError{Name: "validation_error", Message: SanitizeAPIError(err, "Failed to save schedule."), Code: strPtr("STORAGE_ERROR")}
}
