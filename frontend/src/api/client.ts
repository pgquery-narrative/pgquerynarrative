const BASE = "/api/v1";

import { authFetchInit } from "./auth";

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

export interface Column { name: string; type: string; }
export interface ChartSuggestion { chart_type: string; label: string; reason: string; }
export interface RunQueryResult {
  columns: Column[];
  rows: unknown[][];
  row_count: number;
  execution_time_ms: number;
  limit: number;
  chart_suggestions?: ChartSuggestion[];
}

/** Notable node from POST /queries/explain. */
export interface PlanFinding {
  node_type: string;
  schema?: string;
  relation?: string;
  estimated_cost?: number;
  is_seq_scan: boolean;
  category?: string;
  confidence?: string;
  message: string;
  evidence?: string[];
  related_columns?: string[];
  index_advice?: IndexAdvice;
}

export interface IndexDefinition {
  name: string;
  definition: string;
  key_columns?: string[];
  include_columns?: string[];
  predicate?: string;
  is_unique: boolean;
  is_primary: boolean;
  is_valid: boolean;
  size_bytes?: number;
  index_scans?: number;
  tuples_read?: number;
  tuples_fetched?: number;
}

export interface IndexAdvice {
  related_columns?: string[];
  related_indexes?: IndexDefinition[];
  issues?: string[];
  potential_benefit?: string;
  write_cost?: string;
  storage_cost?: string;
  candidate_ddl?: string;
}

export interface RewriteCandidate {
  sql: string;
  rationale: string;
  category?: string;
  confidence?: string;
}

export interface RankedCandidateBaseline {
  total_cost?: number;
  partitions_scanned?: number;
  execution_time_ms?: number;
}

export interface RankedCandidate {
  kind: string;
  rank?: number;
  rankable: boolean;
  sql?: string;
  ddl?: string;
  rationale: string;
  category?: string;
  confidence?: string;
  total_cost?: number;
  cost_delta?: number;
  partitions_scanned?: number;
  partitions_delta?: number;
  execution_time_ms?: number;
  improved?: string[];
}

export interface RankedCandidateList {
  baseline?: RankedCandidateBaseline;
  candidates: RankedCandidate[];
}

/** Result of POST /queries/explain (EXPLAIN FORMAT JSON). */
export interface ExplainQueryResult {
  sql: string;
  total_cost: number;
  plan: unknown;
  findings: PlanFinding[];
  execution_time_ms: number;
}

export interface PlanComparisonMetric {
  evidence: string;
  before: string;
  after: string;
  change: string;
}

export interface PlanComparisonDiff {
  removed?: string[];
  added?: string[];
  improved?: string[];
}

export interface ComparePlansResult {
  before: ExplainQueryResult;
  after: ExplainQueryResult;
  metrics: PlanComparisonMetric[];
  diff: PlanComparisonDiff;
  result_checksum_equal?: boolean;
}

export interface StatSnapshot {
  queryid?: string;
  calls?: number;
  mean_time_ms?: number;
  total_time_ms?: number;
  rows?: number;
}

export interface Investigation {
  id: string;
  title: string;
  status: string;
  sql: string;
  connection_id: string;
  query_fingerprint?: string;
  stat_snapshot?: StatSnapshot;
  explain?: ExplainQueryResult;
  candidate_sql?: string;
  candidate_explain?: ExplainQueryResult;
  comparison?: ComparePlansResult;
  report_id?: string;
  created_at: string;
  updated_at: string;
}

export interface WorkspaceOverview {
  queries_observed: number;
  database_time_hours: number;
  queries_attention: number;
  largest_regression_pct: number;
  temp_data_written_gb: number;
  sequential_scans_detected: number;
  investigations_open: number;
  reports_generated: number;
}

export interface RegressionAlert {
  id: string;
  title: string;
  query: string;
  change_type: string;
  change_summary: string;
  impact: string;
  first_detected_at: string;
  acknowledged: boolean;
  connection_id: string;
}

export interface DemoScenario {
  id: string;
  title: string;
  problem: string;
  sql: string;
  candidate_sql?: string;
  expected_improvement: string;
  category: string;
}

export interface SecurityTrust {
  authentication: string;
  connection_mode: string;
  allowed_schemas: string[];
  tenant_isolation: string;
  tls: string;
  audit_mode: string;
  query_timeout_seconds: number;
  result_limit: number;
  explain_analyze: string;
  external_llm_data: string;
}

export interface SavedQuery {
  id: string;
  name: string;
  sql: string;
  description?: string;
  tags?: string[];
  connection_id: string;
  created_at: string;
  updated_at?: string;
}

export interface NarrativeContent {
  headline: string;
  takeaways?: string[];
  drivers?: string[];
  limitations?: string[];
  recommendations?: string[];
}

export interface Report {
  id: string;
  sql: string;
  narrative: NarrativeContent;
  metrics: Record<string, unknown>;
  chart_suggestions?: ChartSuggestion[];
  connection_id: string;
  created_at: string;
  llm_model: string;
  llm_provider: string;
}

export interface SimilarReportItem {
  id: string;
  headline: string;
  sql: string;
  connection_id: string;
  created_at: string;
  similarity: number;
}
export interface ReportShareLink {
  token: string;
  url: string;
  expires_at?: string;
}
export interface ReportShareInfo {
  id: string;
  created_at: string;
  expires_at?: string;
  revoked_at?: string;
  access_count: number;
  last_accessed_at?: string;
}
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
export interface DashboardWidgetInput {
  widget_type: "report" | "saved_query";
  title?: string;
  report_id?: string;
  saved_query_id?: string;
  refresh_seconds?: number;
  position?: number;
}
export interface DashboardWidget extends DashboardWidgetInput {
  id: string;
  refresh_seconds: number;
  position: number;
}
export interface Dashboard {
  id: string;
  name: string;
  widgets: DashboardWidget[];
  created_at: string;
  updated_at: string;
}
export interface DashboardResolvedWidget {
  id: string;
  widget_type: "report" | "saved_query";
  title?: string;
  refresh_seconds: number;
  position: number;
  report?: Report;
  saved_query?: SavedQuery;
}
export interface DashboardResolved {
  id: string;
  name: string;
  widgets: DashboardResolvedWidget[];
}
export interface Schedule {
  id: string;
  name: string;
  saved_query_id?: string;
  sql?: string;
  connection_id: string;
  interval_expr: string;
  destination_type: string;
  destination_target: string;
  enabled: boolean;
  last_run_at?: string;
  last_status?: string;
  last_error?: string;
  next_run_at?: string;
  created_at: string;
  updated_at: string;
}

export interface ScheduleRun {
  id: string;
  schedule_id: string;
  status: string;
  attempt_count: number;
  scheduled_for: string;
  started_at?: string;
  completed_at?: string;
  report_id?: string;
  failure_code?: string;
  failure_message?: string;
}

export interface WebhookDelivery {
  id: string;
  schedule_id?: string;
  destination_url: string;
  status: string;
  attempt_count: number;
  http_status?: number;
  error_message?: string;
  created_at: string;
  completed_at?: string;
}

export interface AskResult {
  question: string;
  sql: string;
  report: Report;
}
export interface ChatTurn {
  question: string;
  sql: string;
  created_at: string;
}
export interface ChatResult {
  session_id: string;
  question: string;
  sql: string;
  report: Report;
  history: ChatTurn[];
  follow_ups: string[];
}

export interface ConnectionInfo { id: string; name: string; }

export interface StatStatementRow {
  queryid?: string;
  query: string;
  calls: number;
  total_time_ms: number;
  mean_time_ms: number;
  rows: number;
}

export interface StatStatementsResult {
  items: StatStatementRow[];
  order_by: string;
  limit: number;
}

export interface SchemaInfo { name: string; tables: { name: string; columns: Column[] }[]; }

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

  addInvestigationCandidate: (id: string, candidateSql: string, analyze = true) =>
    request<Investigation>(`/investigations/${id}/candidate`, {
      method: "POST",
      body: JSON.stringify({ candidate_sql: candidateSql, analyze }),
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
