package service

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	queriesapi "github.com/pgquerynarrative/pgquerynarrative/api/gen/queries"
	reportsapi "github.com/pgquerynarrative/pgquerynarrative/api/gen/reports"
	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/app/db"
	"github.com/pgquerynarrative/pgquerynarrative/gen/dashboards"
)

type DashboardsService struct {
	appPool    db.DB
	reportsSvc *ReportsService
	queriesSvc *QueriesService
}

func NewDashboardsService(appPool db.DB, reportsSvc *ReportsService, queriesSvc *QueriesService) *DashboardsService {
	return &DashboardsService{appPool: appPool, reportsSvc: reportsSvc, queriesSvc: queriesSvc}
}

func (s *DashboardsService) List(ctx context.Context) (*dashboards.DashboardListResult, error) {
	p := auth.PrincipalFromContext(ctx)
	visPred := visibleResourcePredicate(1, 2, p.Role)
	rows, err := s.appPool.Query(ctx, `SELECT id FROM app.dashboards WHERE `+visPred+` ORDER BY updated_at DESC`, p.OrgID, p.UserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := &dashboards.DashboardListResult{Items: []*dashboards.Dashboard{}}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		item, err := s.Get(ctx, &dashboards.GetPayload{ID: id})
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, item)
	}
	return out, rows.Err()
}

func (s *DashboardsService) Create(ctx context.Context, payload *dashboards.CreatePayload) (*dashboards.Dashboard, error) {
	p := auth.PrincipalFromContext(ctx)
	var id string
	if err := s.appPool.QueryRow(ctx, `
		INSERT INTO app.dashboards (name, organization_id, created_by, visibility)
		VALUES ($1, $2, $3, 'organization')
		RETURNING id
	`, payload.Name, p.OrgID, p.UserID).Scan(&id); err != nil {
		return nil, err
	}
	return s.Get(ctx, &dashboards.GetPayload{ID: id})
}

func (s *DashboardsService) Get(ctx context.Context, payload *dashboards.GetPayload) (*dashboards.Dashboard, error) {
	row := s.appPool.QueryRow(ctx, `
		SELECT id, name, created_at, updated_at
		FROM app.dashboards
		WHERE id = $1 AND organization_id = $2
	`, payload.ID, orgID(ctx))
	var d dashboards.Dashboard
	var createdAt, updatedAt time.Time
	if err := row.Scan(&d.ID, &d.Name, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, &dashboards.NotFoundError{Name: "not_found", Message: "dashboard not found", Code: strPtr("NOT_FOUND")}
		}
		return nil, err
	}
	d.CreatedAt = createdAt.Format(time.RFC3339)
	d.UpdatedAt = updatedAt.Format(time.RFC3339)
	widgets, err := s.getWidgets(ctx, d.ID)
	if err != nil {
		return nil, err
	}
	d.Widgets = widgets
	return &d, nil
}

func (s *DashboardsService) Update(ctx context.Context, payload *dashboards.UpdatePayload) (*dashboards.Dashboard, error) {
	tag, err := s.appPool.Exec(ctx, `
		UPDATE app.dashboards SET name = $2, updated_at = NOW()
		WHERE id = $1 AND organization_id = $3
	`, payload.ID, payload.Name, orgID(ctx))
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, &dashboards.NotFoundError{Name: "not_found", Message: "dashboard not found", Code: strPtr("NOT_FOUND")}
	}
	if payload.Widgets != nil {
		if _, err := s.appPool.Exec(ctx, `DELETE FROM app.dashboard_widgets WHERE dashboard_id = $1`, payload.ID); err != nil {
			return nil, err
		}
		for i, w := range payload.Widgets {
			if w == nil {
				continue
			}
			refresh := int32(300)
			if w.RefreshSeconds != nil && *w.RefreshSeconds > 0 {
				refresh = *w.RefreshSeconds
			}
			position := int32(i)
			if w.Position != nil {
				position = *w.Position
			}
			_, err := s.appPool.Exec(ctx, `
				INSERT INTO app.dashboard_widgets (dashboard_id, widget_type, title, report_id, saved_query_id, refresh_seconds, position)
				VALUES ($1, $2, $3, $4, $5, $6, $7)
			`, payload.ID, w.WidgetType, w.Title, w.ReportID, w.SavedQueryID, refresh, position)
			if err != nil {
				return nil, err
			}
		}
	}
	return s.Get(ctx, &dashboards.GetPayload{ID: payload.ID})
}

func (s *DashboardsService) Delete(ctx context.Context, payload *dashboards.DeletePayload) error {
	p := auth.PrincipalFromContext(ctx)
	var createdBy string
	err := s.appPool.QueryRow(ctx, `
		SELECT COALESCE(created_by, '') FROM app.dashboards
		WHERE id = $1 AND organization_id = $2
	`, payload.ID, p.OrgID).Scan(&createdBy)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return &dashboards.NotFoundError{Name: "not_found", Message: "dashboard not found", Code: strPtr("NOT_FOUND")}
		}
		return err
	}
	if !canMutateOwnedResource(ctx, createdBy) {
		return &dashboards.NotFoundError{Name: "not_found", Message: "dashboard not found", Code: strPtr("NOT_FOUND")}
	}
	_, err = s.appPool.Exec(ctx, `DELETE FROM app.dashboards WHERE id = $1 AND organization_id = $2`, payload.ID, p.OrgID)
	return err
}

func (s *DashboardsService) Resolve(ctx context.Context, payload *dashboards.ResolvePayload) (*dashboards.DashboardResolved, error) {
	d, err := s.Get(ctx, &dashboards.GetPayload{ID: payload.ID})
	if err != nil {
		return nil, err
	}
	res := &dashboards.DashboardResolved{ID: d.ID, Name: d.Name, Widgets: []*dashboards.DashboardResolvedWidget{}}
	for _, w := range d.Widgets {
		if w == nil {
			continue
		}
		item := &dashboards.DashboardResolvedWidget{
			ID:             w.ID,
			WidgetType:     w.WidgetType,
			Title:          w.Title,
			RefreshSeconds: w.RefreshSeconds,
			Position:       w.Position,
		}
		if w.ReportID != nil {
			r, err := s.reportsSvc.Get(ctx, &reportsapi.GetPayload{ID: *w.ReportID})
			if err == nil {
				item.Report = &dashboards.Report{
					ID:               r.ID,
					SavedQueryID:     r.SavedQueryID,
					SQL:              r.SQL,
					ConnectionID:     r.ConnectionID,
					Narrative:        toDashboardNarrative(r.Narrative),
					Metrics:          toDashboardMetrics(r.Metrics),
					ChartSuggestions: toDashboardChartSuggestions(r.ChartSuggestions),
					CreatedAt:        r.CreatedAt,
					LlmModel:         r.LlmModel,
					LlmProvider:      r.LlmProvider,
				}
			}
		}
		if w.SavedQueryID != nil {
			q, err := s.queriesSvc.GetSaved(ctx, &queriesapi.GetSavedPayload{ID: *w.SavedQueryID})
			if err == nil {
				item.SavedQuery = &dashboards.SavedQuery{
					ID:           q.ID,
					Name:         q.Name,
					SQL:          q.SQL,
					Description:  q.Description,
					Tags:         q.Tags,
					ConnectionID: q.ConnectionID,
					CreatedAt:    q.CreatedAt,
					UpdatedAt:    q.UpdatedAt,
				}
			}
		}
		res.Widgets = append(res.Widgets, item)
	}
	return res, nil
}

func (s *DashboardsService) getWidgets(ctx context.Context, dashboardID string) ([]*dashboards.DashboardWidget, error) {
	rows, err := s.appPool.Query(ctx, `
		SELECT id, widget_type, title, report_id, saved_query_id, refresh_seconds, position
		FROM app.dashboard_widgets
		WHERE dashboard_id = $1
		ORDER BY position ASC, created_at ASC
	`, dashboardID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*dashboards.DashboardWidget{}
	for rows.Next() {
		var w dashboards.DashboardWidget
		var title sql.NullString
		var reportID sql.NullString
		var savedID sql.NullString
		if err := rows.Scan(&w.ID, &w.WidgetType, &title, &reportID, &savedID, &w.RefreshSeconds, &w.Position); err != nil {
			return nil, err
		}
		if title.Valid {
			w.Title = &title.String
		}
		if reportID.Valid {
			w.ReportID = &reportID.String
		}
		if savedID.Valid {
			w.SavedQueryID = &savedID.String
		}
		out = append(out, &w)
	}
	return out, rows.Err()
}

func toDashboardNarrative(n *reportsapi.NarrativeContent) *dashboards.NarrativeContent {
	if n == nil {
		return nil
	}
	return &dashboards.NarrativeContent{
		Headline:        n.Headline,
		Takeaways:       n.Takeaways,
		Drivers:         n.Drivers,
		Limitations:     n.Limitations,
		Recommendations: n.Recommendations,
	}
}

func toDashboardMetrics(m *reportsapi.MetricsData) *dashboards.MetricsData {
	if m == nil {
		return nil
	}
	out := &dashboards.MetricsData{
		Aggregates:          map[string]*dashboards.AggregateData{},
		TopCategories:       map[string][]*dashboards.TopCategoryData{},
		TimeSeries:          map[string]*dashboards.TimeSeriesData{},
		Correlations:        make([]*dashboards.CorrelationPairData, 0, len(m.Correlations)),
		Cohorts:             make([]*dashboards.CohortMetricData, 0, len(m.Cohorts)),
		PeriodCurrentLabel:  m.PeriodCurrentLabel,
		PeriodPreviousLabel: m.PeriodPreviousLabel,
		DataQuality:         map[string]*dashboards.ColumnQualityData{},
		PerfSuggestions:     m.PerfSuggestions,
	}

	for k, v := range m.Aggregates {
		if v == nil {
			continue
		}
		out.Aggregates[k] = &dashboards.AggregateData{Sum: v.Sum, Avg: v.Avg, Min: v.Min, Max: v.Max, Count: v.Count, StdDev: v.StdDev}
	}
	for k, values := range m.TopCategories {
		mapped := make([]*dashboards.TopCategoryData, 0, len(values))
		for _, v := range values {
			if v == nil {
				continue
			}
			mapped = append(mapped, &dashboards.TopCategoryData{Category: v.Category, Value: v.Value, Percentage: v.Percentage})
		}
		out.TopCategories[k] = mapped
	}
	for k, v := range m.TimeSeries {
		if v == nil {
			continue
		}
		out.TimeSeries[k] = &dashboards.TimeSeriesData{
			CurrentPeriod:              v.CurrentPeriod,
			PreviousPeriod:             v.PreviousPeriod,
			Change:                     v.Change,
			ChangePercentage:           v.ChangePercentage,
			Trend:                      v.Trend,
			Periods:                    toDashboardPeriodPoints(v.Periods),
			MovingAverage:              v.MovingAverage,
			Anomalies:                  toDashboardAnomalyPoints(v.Anomalies),
			TrendSummary:               toDashboardTrendSummary(v.TrendSummary),
			NextPeriodForecast:         v.NextPeriodForecast,
			ForecastCiLower:            v.ForecastCiLower,
			ForecastCiUpper:            v.ForecastCiUpper,
			PredictiveSummary:          v.PredictiveSummary,
			ExponentialSmoothForecast:  v.ExponentialSmoothForecast,
			HoltForecast:               v.HoltForecast,
			SeasonalPeriod:             v.SeasonalPeriod,
			SeasonallyAdjustedForecast: v.SeasonallyAdjustedForecast,
		}
	}
	for _, c := range m.Correlations {
		if c == nil {
			continue
		}
		out.Correlations = append(out.Correlations, &dashboards.CorrelationPairData{
			ColumnA: c.ColumnA, ColumnB: c.ColumnB, Pearson: c.Pearson, Spearman: c.Spearman,
		})
	}
	for _, c := range m.Cohorts {
		if c == nil {
			continue
		}
		out.Cohorts = append(out.Cohorts, &dashboards.CohortMetricData{
			CohortLabel: c.CohortLabel, Periods: toDashboardCohortPeriods(c.Periods), RetentionPct: c.RetentionPct,
		})
	}
	for k, v := range m.DataQuality {
		if v == nil {
			continue
		}
		out.DataQuality[k] = &dashboards.ColumnQualityData{
			NullCount: v.NullCount, DistinctCount: v.DistinctCount, TotalRows: v.TotalRows, NullPct: v.NullPct,
		}
	}
	return out
}

func toDashboardChartSuggestions(in []*reportsapi.ChartSuggestion) []*dashboards.ChartSuggestion {
	out := make([]*dashboards.ChartSuggestion, 0, len(in))
	for _, c := range in {
		if c == nil {
			continue
		}
		out = append(out, &dashboards.ChartSuggestion{ChartType: c.ChartType, Label: c.Label, Reason: c.Reason})
	}
	return out
}

func toDashboardPeriodPoints(in []*reportsapi.PeriodPointData) []*dashboards.PeriodPointData {
	out := make([]*dashboards.PeriodPointData, 0, len(in))
	for _, p := range in {
		if p == nil {
			continue
		}
		out = append(out, &dashboards.PeriodPointData{Label: p.Label, Value: p.Value})
	}
	return out
}

func toDashboardAnomalyPoints(in []*reportsapi.AnomalyPointData) []*dashboards.AnomalyPointData {
	out := make([]*dashboards.AnomalyPointData, 0, len(in))
	for _, p := range in {
		if p == nil {
			continue
		}
		out = append(out, &dashboards.AnomalyPointData{
			PeriodLabel: p.PeriodLabel, Value: p.Value, Reason: p.Reason, Explanation: p.Explanation,
		})
	}
	return out
}

func toDashboardTrendSummary(in *reportsapi.TrendSummaryData) *dashboards.TrendSummaryData {
	if in == nil {
		return nil
	}
	return &dashboards.TrendSummaryData{
		Direction: in.Direction, Slope: in.Slope, PeriodsUsed: in.PeriodsUsed, Summary: in.Summary, Explanation: in.Explanation,
	}
}

func toDashboardCohortPeriods(in []*reportsapi.CohortPeriodPointData) []*dashboards.CohortPeriodPointData {
	out := make([]*dashboards.CohortPeriodPointData, 0, len(in))
	for _, p := range in {
		if p == nil {
			continue
		}
		out = append(out, &dashboards.CohortPeriodPointData{PeriodLabel: p.PeriodLabel, Value: p.Value})
	}
	return out
}
