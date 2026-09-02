import type * as S from "./schema.gen";
import { authFetchInit } from "./auth";

const BASE = "/api/v1";

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(BASE + path, authFetchInit({
    headers: { "Content-Type": "application/json", ...init?.headers },
    ...init,
  }));
  if (!res.ok) {
    const body = await res.json().catch(() => ({})) as Record<string, unknown>;
    const message =
      (typeof body?.message === "string" && body.message) ||
      (body?.body && typeof (body.body as Record<string, unknown>).message === "string" && (body.body as Record<string, unknown>).message) ||
      (typeof body?.name === "string" && body.name) ||
      res.statusText;
    throw new ApiError(res.status, message as string, body);
  }
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  if (!text.trim()) return undefined as T;
  return JSON.parse(text) as T;
}

export class ApiError extends Error {
  constructor(public status: number, message: string, public body?: unknown) {
    super(message);
  }
}

/*
 * Domain types are generated from the committed OpenAPI spec
 * (api/gen/http/openapi3.json -> schema.gen.ts via `make generate`). Re-export
 * them under their historical names so call sites are unchanged. A handful of
 * types are kept hand-written below: ones the spec does not describe (settings,
 * managed API keys) and ones where the frontend deliberately narrows a field.
 */
export type Column = S.ColumnInfo;
export type ChartSuggestion = S.ChartSuggestion;
export type RunQueryResult = S.RunQueryResult;
export type PlanFinding = S.PlanFinding;
export type IndexDefinition = S.IndexDefinition;
export type IndexAdvice = S.IndexAdvice;
export type RewriteCandidate = S.RewriteCandidate;
export type RankedCandidateBaseline = S.RankedCandidateBaseline;
export type RankedCandidate = S.RankedCandidate;
export type RankedCandidateList = S.RankedCandidateList;
export type PlanDiagnosisCause = S.PlanDiagnosisCause;
export type PlanDiagnosisIncidental = S.PlanDiagnosisIncidental;
export type PlanDiagnosis = S.PlanDiagnosis;
export type ExplainQueryResult = S.ExplainQueryResult;
export type PlanComparisonMetric = S.PlanComparisonMetric;
export type PlanComparisonDiff = S.PlanComparisonDiff;
export type ComparePlansResult = S.ComparePlansResult;
export type StatSnapshot = S.StatSnapshot;
export type InvestigationCandidate = S.InvestigationCandidate;
export type Investigation = S.Investigation;
export type WorkspaceOverview = S.WorkspaceOverview;
export type RegressionAlert = S.RegressionAlert;
export type DemoScenario = S.DemoScenario;
export type SecurityTrust = S.SecurityTrust;
export type SavedQuery = S.SavedQuery;
export type NarrativeContent = S.NarrativeContent;
export type SimilarReportItem = S.SimilarReportItem;
export type ReportShareLink = S.ReportShareLink;
export type ReportShareInfo = S.ReportShareInfo;
export type DashboardWidgetInput = S.DashboardWidgetInput;
export type DashboardWidget = S.DashboardWidget;
export type Dashboard = S.Dashboard;
export type DashboardResolvedWidget = S.DashboardResolvedWidget;
export type DashboardResolved = S.DashboardResolved;
export type Schedule = S.Schedule;
export type ScheduleRun = S.ScheduleRunRecord;
export type WebhookDelivery = S.WebhookDeliveryRecord;
export type AskResult = S.AskResult;
export type ChatTurn = S.ChatTurn;
export type ChatResult = S.ChatResult;
export type ConnectionInfo = S.ConnectionInfo;
export type StatStatementRow = S.StatStatementRow;
export type StatStatementsResult = S.StatStatementsResult;
export type SchemaInfo = S.SchemaInfo;
export type Report = S.Report;

// --- hand-written: not in the OpenAPI spec ---

export interface ManagedAPIKey {
  id: string;
  prefix: string;
  role: string;
  scopes?: string[];
  expires_at?: string;
  revoked_at?: string;
  created_by?: string;
  created_at: string;
}
export interface ManagedAPIKeyIssued extends ManagedAPIKey {
  secret: string;
}

export interface AnalyticsSettings {
  anomaly_sigma: number;
  anomaly_method?: string;
  trend_periods: number;
  moving_avg_window: number;
  trend_threshold_percent: number;
  confidence_level: number;
  min_rows_for_correlation?: number;
  smoothing_alpha?: number;
  smoothing_beta?: number;
  max_seasonal_lag?: number;
  min_periods_for_seasonality?: number;
  max_timeseries_periods?: number;
}

/** LLM section from GET /settings (server env after load). */
export interface LLMSettings {
  provider: string;
  model: string;
  base_url: string;
  api_key_configured: boolean;
}

/** Embedding section from GET /settings. */
export interface EmbeddingSettings {
  enabled: boolean;
  base_url: string;
  model: string;
}

export interface SettingsResponse {
  analytics: AnalyticsSettings;
  llm?: LLMSettings;
  embedding?: EmbeddingSettings;
  security?: { auth_enabled: boolean; rate_limit_rpm: number };
}

// Normalize SQL for API: trim and strip trailing semicolon (API rejects ";" in sql).
function normalizeSql(sql: string): string {
  return sql.trim().replace(/;\s*$/, "");
}

export const api = {
  listStatStatements: (orderBy: "total_time" | "mean_time" | "calls" = "total_time", limit = 20, connectionId?: string) =>
    request<StatStatementsResult>(
      `/queries/stats?order_by=${encodeURIComponent(orderBy)}&limit=${limit}${connectionId ? `&connection_id=${encodeURIComponent(connectionId)}` : ""}`
    ),

  runQuery: (sql: string, limit = 100, connectionId?: string) =>
    request<RunQueryResult>("/queries/run", {
      method: "POST",
      body: JSON.stringify({ sql: normalizeSql(sql), limit, connection_id: connectionId }),
    }),

  explainQuery: (sql: string, analyze = false, connectionId?: string) =>
    request<ExplainQueryResult>("/queries/explain", {
      method: "POST",
      body: JSON.stringify({
        sql: normalizeSql(sql),
        analyze,
        connection_id: connectionId,
      }),
      // ANALYZE can run as long as the query itself; keep headroom for large scans.
      signal: AbortSignal.timeout(analyze ? 120_000 : 60_000),
    }),

  comparePlans: (beforeSql: string, afterSql: string, analyze = false, connectionId?: string) =>
    request<ComparePlansResult>("/queries/explain/compare", {
      method: "POST",
      body: JSON.stringify({
        before_sql: normalizeSql(beforeSql),
        after_sql: normalizeSql(afterSql),
        analyze,
        connection_id: connectionId,
      }),
      signal: AbortSignal.timeout(analyze ? 120_000 : 90_000),
    }),

  listInvestigations: (limit = 20, offset = 0) =>
    request<{ items: Investigation[]; limit: number; offset: number }>(
      `/investigations?limit=${limit}&offset=${offset}`
    ),

  getInvestigation: (id: string) => request<Investigation>(`/investigations/${id}`),

  createInvestigation: (body: {
    title: string;
    sql: string;
    connection_id?: string;
    queryid?: string;
    calls?: number;
    mean_time_ms?: number;
    total_time_ms?: number;
    rows?: number;
  }) => request<Investigation>("/investigations", { method: "POST", body: JSON.stringify(body) }),

  createInvestigationFromRegression: (regressionAlertId: string) =>
    request<Investigation>("/investigations/from-regression", {
      method: "POST",
      body: JSON.stringify({ regression_alert_id: regressionAlertId }),
      signal: AbortSignal.timeout(120_000),
    }),

  addInvestigationCandidate: (id: string, candidateSql: string, analyze = true, binds?: string[]) =>
    request<Investigation>(`/investigations/${id}/candidate`, {
      method: "POST",
      body: JSON.stringify({
        candidate_sql: candidateSql,
        analyze,
        ...(binds && binds.length ? { binds } : {}),
      }),
    }),

  suggestInvestigationRewrite: (id: string) =>
    request<{ candidates: RewriteCandidate[] }>(`/investigations/${id}/suggest-rewrite`, {
      method: "POST",
    }),

  rankInvestigationCandidates: (id: string, analyze = false) =>
    request<RankedCandidateList>(`/investigations/${id}/rank-candidates`, {
      method: "POST",
      body: JSON.stringify({ analyze }),
      signal: AbortSignal.timeout(analyze ? 120_000 : 90_000),
    }),

  generateInvestigationReport: (id: string) =>
    request<Investigation>(`/investigations/${id}/report`, { method: "POST" }),

  updateInvestigationFix: (id: string, body: { fix_status?: string; fix_reference?: string }) =>
    request<Investigation>(`/investigations/${id}/fix`, {
      method: "POST",
      body: JSON.stringify(body),
    }),

  getWorkspaceOverview: () => request<WorkspaceOverview>("/workspace/overview"),

  getRegressions: (limit = 10, includeAcknowledged = false) =>
    request<{ items: RegressionAlert[] }>(
      `/workspace/regressions?limit=${limit}&include_acknowledged=${includeAcknowledged}`
    ),

  acknowledgeRegression: (id: string) =>
    request<void>(`/workspace/regressions/${id}/acknowledge`, { method: "POST" }),

  getDemoScenarios: () => request<{ items: DemoScenario[] }>("/demo/scenarios"),

  getSecurityTrust: () => request<SecurityTrust>("/trust"),

  listSaved: (limit = 50, offset = 0, connectionId?: string) =>
    request<{ items: SavedQuery[]; limit: number; offset: number }>(`/queries/saved?limit=${limit}&offset=${offset}${connectionId ? `&connection_id=${encodeURIComponent(connectionId)}` : ""}`),

  findSimilarQueries: (text: string, limit = 10) =>
    request<{ suggestions: { sql: string; title: string; source: string }[] }>(
      `/suggestions/similar?text=${encodeURIComponent(text)}&limit=${limit}`
    ),

  saveQuery: (name: string, sql: string, tags: string[] = [], connectionId?: string) =>
    request<SavedQuery>("/queries/saved", { method: "POST", body: JSON.stringify({ name, sql, tags, connection_id: connectionId }) }),

  getSaved: (id: string) => request<SavedQuery>(`/queries/saved/${id}`),

  deleteSaved: (id: string) => request<void>(`/queries/saved/${id}`, { method: "DELETE" }),

  generateReport: (sql: string, connectionId?: string) =>
    request<Report>("/reports/generate", {
      method: "POST",
      body: JSON.stringify({ sql: normalizeSql(sql), connection_id: connectionId }),
    }),

  listReports: (limit = 50, offset = 0, connectionId?: string) =>
    request<{ items: Report[]; limit: number; offset: number }>(`/reports?limit=${limit}&offset=${offset}${connectionId ? `&connection_id=${encodeURIComponent(connectionId)}` : ""}`),
  findSimilarReports: (text: string, limit = 5, connectionId?: string) =>
    request<{ items: SimilarReportItem[] }>(`/reports/similar?text=${encodeURIComponent(text)}&limit=${limit}${connectionId ? `&connection_id=${encodeURIComponent(connectionId)}` : ""}`),

  getReport: (id: string) => request<Report>(`/reports/${id}`),
  getSharedReport: (token: string) => request<Report>(`/reports/shared/${encodeURIComponent(token)}`),
  createShareLink: (reportId: string, expiresInHours?: number) =>
    request<ReportShareLink>("/reports/share", {
      method: "POST",
      body: JSON.stringify({ report_id: reportId, expires_in_hours: expiresInHours }),
    }),
  listShares: (reportId: string) =>
    request<{ items: ReportShareInfo[] }>(`/reports/${encodeURIComponent(reportId)}/shares`),
  revokeShare: (shareId: string) =>
    request<void>(`/reports/shares/${encodeURIComponent(shareId)}/revoke`, { method: "POST" }),
  rewriteReport: (reportId: string, instruction: string) =>
    request<NarrativeContent>("/reports/rewrite", {
      method: "POST",
      body: JSON.stringify({ report_id: reportId, instruction: instruction.trim() }),
    }),

  listManagedKeys: () =>
    request<{ items: ManagedAPIKey[] }>("/admin/api-keys"),
  createManagedKey: (body: { role?: string; scopes?: string[]; expires_at?: string }) =>
    request<ManagedAPIKeyIssued>("/admin/api-keys", { method: "POST", body: JSON.stringify(body) }),
  revokeManagedKey: (id: string) =>
    request<void>(`/admin/api-keys/${encodeURIComponent(id)}/revoke`, { method: "POST" }),

  getSchema: (connectionId?: string) => request<{ schemas: SchemaInfo[] }>(`/schema${connectionId ? `?connection_id=${encodeURIComponent(connectionId)}` : ""}`),
  listConnections: () => request<{ items: ConnectionInfo[] }>("/connections"),

  getSettings: () => request<SettingsResponse>("/settings"),

  getSuggestions: (limit = 5) =>
    request<{ suggestions: { sql: string; title: string; source: string }[] }>(`/suggestions/queries?limit=${limit}`),
  getSuggestedQuestions: (limit = 8, connectionId?: string) =>
    request<{ questions: string[] }>(`/suggestions/questions?limit=${limit}${connectionId ? `&connection_id=${encodeURIComponent(connectionId)}` : ""}`),

  ask: (question: string, connectionId?: string) =>
    request<AskResult>("/suggestions/ask", {
      method: "POST",
      body: JSON.stringify({ question: question.trim(), connection_id: connectionId }),
      signal: AbortSignal.timeout(240_000),
    }),
  askChat: (question: string, sessionId?: string, connectionId?: string) =>
    request<ChatResult>("/suggestions/chat", {
      method: "POST",
      body: JSON.stringify({ question: question.trim(), session_id: sessionId, connection_id: connectionId }),
      signal: AbortSignal.timeout(240_000),
    }),

  listDashboards: () => request<{ items: Dashboard[] }>("/dashboards"),
  createDashboard: (name: string) =>
    request<Dashboard>("/dashboards", { method: "POST", body: JSON.stringify({ name }) }),
  getDashboard: (id: string) => request<Dashboard>(`/dashboards/${id}`),
  updateDashboard: (id: string, name: string, widgets: DashboardWidgetInput[]) =>
    request<Dashboard>(`/dashboards/${id}`, { method: "PUT", body: JSON.stringify({ name, widgets }) }),
  deleteDashboard: (id: string) => request<void>(`/dashboards/${id}`, { method: "DELETE" }),
  resolveDashboard: (id: string) => request<DashboardResolved>(`/dashboards/${id}/resolve`),

  listSchedules: () => request<{ items: Schedule[] }>("/schedules"),
  createSchedule: (payload: Record<string, unknown>) => request<Schedule>("/schedules", { method: "POST", body: JSON.stringify(payload) }),
  updateSchedule: (id: string, payload: Record<string, unknown>) => request<Schedule>(`/schedules/${id}`, { method: "PUT", body: JSON.stringify(payload) }),
  deleteSchedule: (id: string) => request<void>(`/schedules/${id}`, { method: "DELETE" }),
  runScheduleNow: (id: string) => request<{ schedule: Schedule; report_id?: string; delivered: boolean }>(`/schedules/${id}/run`, { method: "POST" }),
  listScheduleRuns: (id: string) => request<{ items: ScheduleRun[] }>(`/schedules/${encodeURIComponent(id)}/runs`),
  retryScheduleRun: (runId: string) =>
    request<ScheduleRun>(`/schedule-runs/${encodeURIComponent(runId)}/retry`, { method: "POST" }),
  listWebhookDeliveries: () => request<{ items: WebhookDelivery[] }>("/webhook-deliveries"),
};
