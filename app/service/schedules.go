package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	queriesapi "github.com/pgquerynarrative/pgquerynarrative/api/gen/queries"
	reportsapi "github.com/pgquerynarrative/pgquerynarrative/api/gen/reports"
	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/app/db"
	"github.com/pgquerynarrative/pgquerynarrative/app/observability"
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
}

func NewSchedulesService(appPool db.DB, queriesSvc *QueriesService, reportsSvc *ReportsService) *SchedulesService {
	return &SchedulesService{
		appPool:    appPool,
		queriesSvc: queriesSvc,
		reportsSvc: reportsSvc,
	}
}

// SetRawPool sets the underlying pool for cross-org scheduler claims and retries.
func (s *SchedulesService) SetRawPool(pool *pgxpool.Pool) {
	if s != nil {
		s.rawPool = pool
	}
}

// SetWebhookSigningSecret configures signed outbound webhook delivery.
func (s *SchedulesService) SetWebhookSigningSecret(secret string, allowedHosts ...string) {
	s.webhookSecret = strings.TrimSpace(secret)
	s.allowedHosts = allowedHosts
	s.webhookClient = security.NewWebhookClient(s.webhookSecret, 10*time.Second, allowedHosts...)
}

func (s *SchedulesService) webhookAllowedHosts() []string {
	if s == nil {
		return nil
	}
	return s.allowedHosts
}

func (s *SchedulesService) List(ctx context.Context) (*schedules.ScheduleListResult, error) {
	rows, err := s.appPool.Query(ctx, `
		SELECT id, name, saved_query_id, sql, connection_id, cron_expr, destination_type, destination_target, enabled,
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
		items = append(items, item)
	}
	return &schedules.ScheduleListResult{Items: items}, rows.Err()
}

func (s *SchedulesService) Create(ctx context.Context, payload *schedules.ScheduleInput) (*schedules.Schedule, error) {
	if err := validateScheduleInput(payload, s.webhookAllowedHosts()); err != nil {
		return nil, &schedules.ValidationError{Name: "validation_error", Message: err.Error(), Code: strPtr("VALIDATION_ERROR")}
	}
	if err := s.validateScheduleSQL(ctx, payload); err != nil {
		return nil, &schedules.ValidationError{Name: "validation_error", Message: err.Error(), Code: strPtr("VALIDATION_ERROR")}
	}
	nextRunAt, err := computeNextRun(payload.CronExpr, time.Now().UTC())
	if err != nil {
		return nil, &schedules.ValidationError{Name: "validation_error", Message: err.Error(), Code: strPtr("VALIDATION_ERROR")}
	}
	var id string
	p := auth.PrincipalFromContext(ctx)
	err = s.appPool.QueryRow(ctx, `
		INSERT INTO app.schedules (name, saved_query_id, sql, connection_id, cron_expr, destination_type, destination_target, enabled, next_run_at, organization_id, created_by)
		VALUES ($1, $2, NULLIF($3, ''), COALESCE(NULLIF($4, ''), 'default'), $5, $6, $7, COALESCE($8, true), $9, $10, $11)
		RETURNING id
	`, payload.Name, payload.SavedQueryID, strings.TrimSpace(ptrString(payload.SQL)), payload.ConnectionID, payload.CronExpr, payload.DestinationType, payload.DestinationTarget, payload.Enabled, nextRunAt, p.OrgID, p.UserID).Scan(&id)
	if err != nil {
		return nil, err
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
		CronExpr:          firstNonBlank(payload.CronExpr, current.CronExpr),
		DestinationType:   firstNonBlank(payload.DestinationType, current.DestinationType),
		DestinationTarget: firstNonBlank(payload.DestinationTarget, current.DestinationTarget),
		Enabled:           coalesceBoolPtr(payload.Enabled, current.Enabled),
	}
	if err := validateScheduleInput(in, s.webhookAllowedHosts()); err != nil {
		return nil, &schedules.ValidationError{Name: "validation_error", Message: err.Error(), Code: strPtr("VALIDATION_ERROR")}
	}
	if err := s.validateScheduleSQL(ctx, in); err != nil {
		return nil, &schedules.ValidationError{Name: "validation_error", Message: err.Error(), Code: strPtr("VALIDATION_ERROR")}
	}
	nextRunAt, err := computeNextRun(in.CronExpr, time.Now().UTC())
	if err != nil {
		return nil, &schedules.ValidationError{Name: "validation_error", Message: err.Error(), Code: strPtr("VALIDATION_ERROR")}
	}
	_, err = s.appPool.Exec(ctx, `
		UPDATE app.schedules
		SET name = $2, saved_query_id = $3, sql = NULLIF($4, ''), connection_id = COALESCE(NULLIF($5, ''), 'default'),
		    cron_expr = $6, destination_type = $7, destination_target = $8, enabled = COALESCE($9, enabled),
		    next_run_at = $10, updated_at = NOW()
		WHERE id = $1 AND organization_id = $11
	`, payload.ID, in.Name, in.SavedQueryID, in.SQL, in.ConnectionID, in.CronExpr, in.DestinationType, in.DestinationTarget, in.Enabled, nextRunAt, orgID(ctx))
	if err != nil {
		return nil, err
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
	reportID, delivered, runErr := s.runSchedule(ctx, sc)
	status := "success"
	lastErr := ""
	if runErr != nil {
		status = "failed"
		lastErr = SanitizeStoredError(runErr)
	}
	nextRun, _ := computeNextRun(sc.CronExpr, time.Now().UTC())
	_, _ = s.appPool.Exec(ctx, `
		UPDATE app.schedules
		SET last_run_at = NOW(), last_status = $2, last_error = NULLIF($3, ''), next_run_at = $4, updated_at = NOW()
		WHERE id = $1
	`, sc.ID, status, lastErr, nextRun)
	updated, _ := s.getByID(ctx, sc.ID)
	return &schedules.ScheduleRunResult{Schedule: updated, ReportID: strPtrIfNotEmpty(reportID), Delivered: delivered}, runErr
}

func (s *SchedulesService) runSchedule(ctx context.Context, sc *schedules.Schedule) (string, bool, error) {
	sqlText := strings.TrimSpace(ptrString(sc.SQL))
	if sc.SavedQueryID != nil && *sc.SavedQueryID != "" {
		sq, err := s.queriesSvc.GetSaved(ctx, &queriesapi.GetSavedPayload{ID: *sc.SavedQueryID})
		if err != nil {
			return "", false, err
		}
		sqlText = sq.SQL
	}
	if sqlText == "" {
		return "", false, errors.New("schedule has neither SQL nor valid saved query")
	}
	report, err := s.reportsSvc.Generate(ctx, &reportsapi.GenerateReportPayload{SQL: sqlText, ConnectionID: strPtrIfNotEmpty(sc.ConnectionID)})
	if err != nil {
		return "", false, err
	}
	delivered, err := s.deliverReport(ctx, sc, report.ID)
	return report.ID, delivered, err
}

func (s *SchedulesService) deliverReport(ctx context.Context, sc *schedules.Schedule, reportID string) (bool, error) {
	switch strings.ToLower(sc.DestinationType) {
	case "log":
		return true, nil
	case "webhook":
		if strings.TrimSpace(sc.DestinationTarget) == "" {
			return false, errors.New("webhook target is required")
		}
		client := s.webhookClient
		if client == nil {
			client = security.NewWebhookClient("", 10*time.Second, s.allowedHosts...)
		}
		deliveryID := sc.ID + ":" + reportID
		payload := map[string]any{
			"schedule_id": sc.ID,
			"report_id":   reportID,
		}
		observability.IncWebhookDelivery()
		result, err := client.PostJSON(ctx, sc.DestinationTarget, deliveryID, payload)
		status := "delivered"
		errMsg := ""
		httpStatus := 0
		respBytes := 0
		if err != nil {
			status = "failed"
			errMsg = err.Error()
		} else {
			httpStatus = result.StatusCode
			respBytes = result.ResponseBytes
			if result.StatusCode >= 300 {
				status = "failed"
				errMsg = "webhook delivery failed"
			}
		}
		idempotencyKey := deliveryID
		payloadJSON, _ := json.Marshal(payload)
		_, _ = s.appPool.Exec(ctx, `
			INSERT INTO app.webhook_deliveries (
				organization_id, schedule_id, destination_url, idempotency_key, payload, status,
				attempt_count, http_status, response_bytes, error_message, completed_at
			) VALUES ($1,$2,$3,$4,$5,$6,1,$7,$8,NULLIF($9,''),NOW())
			ON CONFLICT (idempotency_key) DO UPDATE SET
				status = EXCLUDED.status,
				attempt_count = app.webhook_deliveries.attempt_count + 1,
				http_status = EXCLUDED.http_status,
				response_bytes = EXCLUDED.response_bytes,
				error_message = EXCLUDED.error_message,
				completed_at = NOW()
		`, orgID(ctx), sc.ID, sc.DestinationTarget, idempotencyKey, payloadJSON, status, nullInt(httpStatus), respBytes, errMsg)
		if status == "failed" {
			return false, errors.New(errMsg)
		}
		return true, nil
	default:
		return false, errors.New("unsupported destination_type")
	}
}

func (s *SchedulesService) getByID(ctx context.Context, id string) (*schedules.Schedule, error) {
	return s.getByIDForOrg(ctx, id, orgID(ctx))
}

func (s *SchedulesService) getByIDForOrg(ctx context.Context, id, organizationID string) (*schedules.Schedule, error) {
	row := s.appPool.QueryRow(ctx, `
		SELECT id, name, saved_query_id, sql, connection_id, cron_expr, destination_type, destination_target, enabled,
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
	return item, nil
}

type scanner interface{ Scan(dest ...any) error }

func scanSchedule(r scanner) (*schedules.Schedule, error) {
	var s schedules.Schedule
	var savedID sql.NullString
	var sqlText sql.NullString
	var lastRun sql.NullTime
	var lastStatus sql.NullString
	var lastError sql.NullString
	var nextRun sql.NullTime
	var createdAt time.Time
	var updatedAt time.Time
	if err := r.Scan(&s.ID, &s.Name, &savedID, &sqlText, &s.ConnectionID, &s.CronExpr, &s.DestinationType, &s.DestinationTarget, &s.Enabled,
		&lastRun, &lastStatus, &lastError, &nextRun, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	if savedID.Valid {
		s.SavedQueryID = &savedID.String
	}
	if sqlText.Valid {
		s.SQL = &sqlText.String
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
	if strings.TrimSpace(in.CronExpr) == "" {
		return errors.New("cron_expr is required")
	}
	if _, err := computeNextRun(in.CronExpr, time.Now().UTC()); err != nil {
		return err
	}
	dt := strings.ToLower(strings.TrimSpace(in.DestinationType))
	if dt != "webhook" && dt != "log" {
		return errors.New("destination_type must be webhook or log")
	}
	if strings.TrimSpace(in.DestinationTarget) == "" {
		return errors.New("destination_target is required")
	}
	if dt == "webhook" {
		if err := security.ValidateWebhookURL(in.DestinationTarget); err != nil {
			return err
		}
		if err := security.ValidateWebhookHostAllowlist(in.DestinationTarget, allowedHosts); err != nil {
			return err
		}
	}
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
		return time.Time{}, errors.New("cron_expr must use '@every <duration>' format")
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
