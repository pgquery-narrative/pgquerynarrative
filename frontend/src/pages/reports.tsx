import { useEffect, useState, useCallback } from "react";
import { useParams, Link } from "react-router";
import { Card, CardHeader, CardTitle, CardContent, CardDescription } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { api, type Report, type SimilarReportItem, type ConnectionInfo, type ReportShareInfo } from "@/api/client";
import { FileText, Download, Clock, Cpu, ArrowLeft, BarChart3, Search, Trash2 } from "lucide-react";
import { Input } from "@/components/ui/input";
import { buttonVariants } from "@/components/ui/button";
import { cn, truncate } from "@/lib/utils";

type TimeSeriesEntry = {
  next_period_forecast?: number | null;
  forecast_ci_lower?: number | null;
  forecast_ci_upper?: number | null;
  trend_summary?: { summary?: string; explanation?: string } | null;
  anomalies?: Array<{ period_label?: string; value?: number; reason?: string; explanation?: string }> | null;
};
type CohortPeriodPoint = { period_label?: string; periodLabel?: string; value?: number };
type CohortEntry = { cohort_label?: string; cohortLabel?: string; periods?: CohortPeriodPoint[]; retention_pct?: number | null; retentionPct?: number | null };
type MetricsPayload = {
  time_series?: Record<string, TimeSeriesEntry> | null;
  correlations?: Array<{ column_a?: string; column_b?: string; pearson?: number; spearman?: number }> | null;
  cohorts?: CohortEntry[] | null;
};

function ReportDetail() {
  const { id, token } = useParams<{ id?: string; token?: string }>();
  const [report, setReport] = useState<Report | null>(null);
  const [loading, setLoading] = useState(true);
  const [rewriteInstruction, setRewriteInstruction] = useState("");
  const [rewriteLoading, setRewriteLoading] = useState(false);
  const [rewriteError, setRewriteError] = useState("");
  const [shareLoading, setShareLoading] = useState(false);
  const [shareExpiry, setShareExpiry] = useState("168");
  const [shareMessage, setShareMessage] = useState("");
  const [lastShareURL, setLastShareURL] = useState("");
  const [shares, setShares] = useState<ReportShareInfo[]>([]);
  const [sharesLoading, setSharesLoading] = useState(false);
  const [revokeId, setRevokeId] = useState<string | null>(null);

  const refreshShares = useCallback(async () => {
    if (!id) return;
    setSharesLoading(true);
    try {
      const res = await api.listShares(id);
      setShares(res.items ?? []);
    } catch {
      setShares([]);
    } finally {
      setSharesLoading(false);
    }
  }, [id]);

  useEffect(() => {
    if (!id && !token) return;
    const loader = token ? api.getSharedReport(token) : api.getReport(id!);
    loader.then(setReport).catch(() => {}).finally(() => setLoading(false));
  }, [id, token]);

  useEffect(() => {
    if (!id || token) return;
    void refreshShares();
  }, [id, token, refreshShares]);

  if (loading) return <div className="space-y-4"><Skeleton className="h-8 w-64" /><Skeleton className="h-64 w-full" /></div>;
  if (!report) return <p className="text-muted-foreground">Report not found.</p>;

  const { narrative } = report;
  const isSharedView = Boolean(token);
  const applyRewrite = async (instruction: string) => {
    const trimmed = instruction.trim();
    if (!trimmed) return;
    setRewriteError("");
    setRewriteLoading(true);
    try {
      const rewritten = await api.rewriteReport(report.id, trimmed);
      setReport({ ...report, narrative: rewritten });
      setRewriteInstruction(trimmed);
    } catch (e) {
      setRewriteError(e instanceof Error ? e.message : "Failed to rewrite narrative");
    } finally {
      setRewriteLoading(false);
    }
  };
  const createShareLink = async () => {
    if (!id) return;
    setShareLoading(true);
    setShareMessage("");
    setLastShareURL("");
    try {
      const expiry = Number(shareExpiry);
      const result = await api.createShareLink(id, Number.isFinite(expiry) && expiry > 0 ? expiry : undefined);
      const absolute = `${window.location.origin}${result.url}`;
      setLastShareURL(result.url);
      try {
        await navigator.clipboard.writeText(absolute);
        setShareMessage(`Share link copied${result.expires_at ? ` (expires ${new Date(result.expires_at).toLocaleString()})` : ""}`);
      } catch {
        setShareMessage(`Share link created${result.expires_at ? ` (expires ${new Date(result.expires_at).toLocaleString()})` : ""}`);
      }
      await refreshShares();
    } catch (e) {
      setShareMessage(e instanceof Error ? e.message : "Failed to create share link");
    } finally {
      setShareLoading(false);
    }
  };
  const revokeShareLink = async (shareId: string) => {
    setRevokeId(shareId);
    try {
      await api.revokeShare(shareId);
      await refreshShares();
      setShareMessage("Share link revoked");
    } catch (e) {
      setShareMessage(e instanceof Error ? e.message : "Failed to revoke share link");
    } finally {
      setRevokeId(null);
    }
  };

  return (
    <div className="space-y-6">
      <Link to="/reports" className="inline-flex items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground transition-colors"><ArrowLeft className="h-4 w-4" /> Back to reports</Link>

      <div className="flex items-start justify-between">
        <div>
          <h1 data-testid={isSharedView ? "shared-report-headline" : "report-detail-headline"} className="text-2xl font-bold tracking-tight">{narrative?.headline || "Report"}</h1>
          <div className="flex items-center gap-3 mt-2 text-xs text-muted-foreground">
            <span className="flex items-center gap-1"><Clock className="h-3 w-3" />{new Date(report.created_at).toLocaleString()}</span>
            <span className="flex items-center gap-1"><Cpu className="h-3 w-3" />{report.llm_provider} / {report.llm_model}</span>
            <Badge variant="secondary" className="text-[10px]">{report.connection_id}</Badge>
          </div>
        </div>
        <div className="flex gap-2">
          {!isSharedView && <a href={`/web/reports/export?id=${report.id}`} download><Button variant="outline" size="sm"><Download className="h-4 w-4" /> HTML</Button></a>}
          <a href={isSharedView ? `/web/reports/export/shared/pdf?token=${encodeURIComponent(token ?? "")}` : `/web/reports/export/pdf?id=${report.id}`} download><Button variant="outline" size="sm"><Download className="h-4 w-4" /> PDF</Button></a>
        </div>
      </div>
      {!isSharedView && (
        <Card data-testid="report-share-panel">
          <CardHeader>
            <CardTitle className="text-sm">Share Report</CardTitle>
            <CardDescription>Create a read-only link with optional expiry. Revoke any active link below.</CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="flex flex-wrap gap-2 items-center">
              <select
                data-testid="share-expiry"
                className="h-10 rounded-md border border-input bg-background px-3 text-sm"
                value={shareExpiry}
                onChange={(e) => setShareExpiry(e.target.value)}
              >
                <option value="24">24 hours</option>
                <option value="72">3 days</option>
                <option value="168">7 days</option>
                <option value="720">30 days</option>
              </select>
              <Button data-testid="share-create" onClick={() => { void createShareLink(); }} disabled={shareLoading}>
                Create & Copy Link
              </Button>
              {shareMessage && <p data-testid="share-message" className="text-xs text-muted-foreground">{shareMessage}</p>}
              {lastShareURL && (
                <p data-testid="share-link-url" data-url={lastShareURL} className="sr-only">
                  {lastShareURL}
                </p>
              )}
            </div>
            <div data-testid="share-list" className="border-t border-border pt-3 space-y-2">
              <p className="text-xs font-medium text-muted-foreground">Active and revoked links</p>
              {sharesLoading ? (
                <Skeleton className="h-10 w-full" />
              ) : shares.length === 0 ? (
                <p className="text-xs text-muted-foreground">No share links yet.</p>
              ) : (
                <ul className="space-y-2">
                  {shares.map((s) => {
                    const revoked = Boolean(s.revoked_at);
                    return (
                      <li
                        key={s.id}
                        data-testid="share-item"
                        data-share-id={s.id}
                        data-revoked={revoked ? "true" : "false"}
                        className="flex flex-wrap items-center justify-between gap-2 text-xs border border-border rounded-md px-3 py-2"
                      >
                        <div className="space-y-0.5">
                          <p className="font-mono">{s.id.slice(0, 8)}…</p>
                          <p className="text-muted-foreground">
                            created {new Date(s.created_at).toLocaleString()}
                            {s.expires_at ? ` · expires ${new Date(s.expires_at).toLocaleString()}` : ""}
                            {` · accessed ${s.access_count}×`}
                            {revoked ? ` · revoked ${new Date(s.revoked_at!).toLocaleString()}` : ""}
                          </p>
                        </div>
                        {!revoked && (
                          <Button
                            data-testid="share-revoke"
                            variant="destructive"
                            size="sm"
                            disabled={revokeId === s.id}
                            onClick={() => { void revokeShareLink(s.id); }}
                          >
                            <Trash2 className="h-3.5 w-3.5" />
                            Revoke
                          </Button>
                        )}
                        {revoked && <Badge variant="secondary">revoked</Badge>}
                      </li>
                    );
                  })}
                </ul>
              )}
            </div>
          </CardContent>
        </Card>
      )}

      <Card>
        <CardHeader><CardTitle className="text-sm text-muted-foreground">SQL Query</CardTitle></CardHeader>
        <CardContent><pre className="p-4 rounded-md bg-background border border-border text-xs font-mono overflow-auto whitespace-pre-wrap">{report.sql}</pre></CardContent>
      </Card>

      {/* Narrative sections */}
      {!isSharedView && <Card className="border-primary/20">
        <CardHeader>
          <CardTitle className="text-sm">Refine Narrative</CardTitle>
          <CardDescription>Rewrite for different audiences without rerunning the query.</CardDescription>
        </CardHeader>
        <CardContent className="pt-0 space-y-3">
          <div className="flex flex-wrap gap-2">
            {["Make it concise", "Focus on risks", "Explain for executives", "Translate to Spanish", "Emphasize anomalies"].map((preset) => (
              <Button key={preset} variant="outline" size="sm" disabled={rewriteLoading} onClick={() => { void applyRewrite(preset); }}>
                {preset}
              </Button>
            ))}
          </div>
          <div className="flex gap-2">
            <Input
              value={rewriteInstruction}
              onChange={(e) => setRewriteInstruction(e.target.value)}
              placeholder="Custom instruction (e.g. make it investor-friendly)"
              disabled={rewriteLoading}
            />
            <Button disabled={rewriteLoading || !rewriteInstruction.trim()} onClick={() => { void applyRewrite(rewriteInstruction); }}>
              Rewrite
            </Button>
          </div>
          {rewriteError && <p className="text-xs text-destructive">{rewriteError}</p>}
        </CardContent>
      </Card>}
      <Card className="border-primary/20">
        <CardContent className="p-6 space-y-5">
          {narrative?.headline && <h2 className="text-xl font-semibold">{narrative.headline}</h2>}

          {narrative?.takeaways && narrative.takeaways.length > 0 && (
            <Section title="Key Takeaways">{narrative.takeaways.map((t, i) => <Li key={i} color="text-primary">{t}</Li>)}</Section>
          )}
          {narrative?.drivers && narrative.drivers.length > 0 && (
            <Section title="Drivers">{narrative.drivers.map((d, i) => <Li key={i} color="text-brand-blue">{d}</Li>)}</Section>
          )}
          {narrative?.limitations && narrative.limitations.length > 0 && (
            <Section title="Limitations">{narrative.limitations.map((l, i) => <Li key={i} color="text-warning">{l}</Li>)}</Section>
          )}
          {narrative?.recommendations && narrative.recommendations.length > 0 && (
            <Section title="Recommendations">{narrative.recommendations.map((r, i) => <Li key={i} color="text-accent">{r}</Li>)}</Section>
          )}
        </CardContent>
      </Card>

      {report.chart_suggestions && report.chart_suggestions.length > 0 && (
        <Card>
          <CardHeader><CardTitle className="text-sm">Suggested Charts</CardTitle></CardHeader>
          <CardContent className="space-y-3">
            <div className="rounded-md border border-primary/20 bg-primary/5 p-3">
              <p className="text-xs uppercase tracking-wide text-muted-foreground">Recommended</p>
              <p className="text-sm font-medium mt-1">{report.chart_suggestions[0]?.label}</p>
              {report.chart_suggestions[0]?.reason && (
                <p className="text-xs text-muted-foreground mt-1">{report.chart_suggestions[0].reason}</p>
              )}
            </div>
            <div className="flex flex-wrap gap-2">
              {report.chart_suggestions.map((s, i) => <Badge key={i} variant="outline">{s.label}</Badge>)}
            </div>
          </CardContent>
        </Card>
      )}

      <ReportMetricsCard metrics={report.metrics as MetricsPayload | undefined} />
    </div>
  );
}

function ReportMetricsCard({ metrics }: { metrics?: MetricsPayload | null }) {
  // Support both snake_case (API) and camelCase (some proxies)
  const m = metrics as Record<string, unknown> | null | undefined;
  if (!m || typeof m !== "object") return null;

  const tsRaw = m.time_series ?? m.timeSeries;
  const corrRaw = m.correlations;
  const cohortsRaw = m.cohorts;
  const ts = tsRaw && typeof tsRaw === "object" && !Array.isArray(tsRaw) && Object.keys(tsRaw as object).length > 0 ? (tsRaw as Record<string, TimeSeriesEntry>) : null;
  const corr = Array.isArray(corrRaw) && corrRaw.length > 0 ? corrRaw as MetricsPayload["correlations"] : null;
  const cohorts = Array.isArray(cohortsRaw) && cohortsRaw.length > 0 ? (cohortsRaw as CohortEntry[]) : null;

  if (!ts && !corr && !cohorts) {
    return (
      <Card>
        <CardHeader className="flex flex-row items-center gap-2">
          <BarChart3 className="h-4 w-4 text-muted-foreground" />
          <CardTitle className="text-sm">Analytics</CardTitle>
        </CardHeader>
        <CardContent>
          <p className="text-sm text-muted-foreground">No time-series, correlation, or cohort metrics for this report. Run a query with a date column and numeric measures (or cohort + period columns), then generate a new report.</p>
        </CardContent>
      </Card>
    );
  }

  const formatNum = (n: number | null | undefined) => n != null ? n.toLocaleString(undefined, { maximumFractionDigits: 2 }) : "—";
  const val = (o: Record<string, unknown>, a: string, b: string) => o[a] ?? o[b];

  return (
    <Card>
      <CardHeader className="flex flex-row items-center gap-2">
        <BarChart3 className="h-4 w-4 text-muted-foreground" />
        <CardTitle className="text-sm">Analytics</CardTitle>
      </CardHeader>
      <CardContent className="space-y-5">
        {ts && (
          <div className="space-y-4">
            <h4 className="text-xs font-semibold uppercase text-muted-foreground tracking-wide">Time series & forecast</h4>
            {Object.entries(ts).map(([measure, data]) => {
              const d = data as Record<string, unknown>;
              const forecast = val(d, "next_period_forecast", "nextPeriodForecast") as number | null | undefined;
              const ciLower = val(d, "forecast_ci_lower", "forecastCiLower") as number | null | undefined;
              const ciUpper = val(d, "forecast_ci_upper", "forecastCiUpper") as number | null | undefined;
              const trendSummary = (val(d, "trend_summary", "trendSummary") as { summary?: string; explanation?: string } | null) ?? null;
              const anomalies = (val(d, "anomalies", "anomalies") as Array<{ period_label?: string; value?: number; reason?: string; explanation?: string }> | null) ?? null;
              return (
                <div key={measure} className="rounded-md border border-border bg-muted/30 p-3 space-y-1.5 text-sm">
                  <p className="font-medium">{measure}</p>
                  {forecast != null && (
                    <p className="text-muted-foreground">Forecast (next period): {formatNum(forecast)}</p>
                  )}
                  {ciLower != null && ciUpper != null && (
                    <p className="text-muted-foreground text-xs">Interval: {formatNum(ciLower)} – {formatNum(ciUpper)}</p>
                  )}
                  {trendSummary?.summary && <p className="text-xs">{trendSummary.summary}</p>}
                  {trendSummary?.explanation && (
                    <p className="text-xs text-muted-foreground">{trendSummary.explanation}</p>
                  )}
                  {anomalies && anomalies.length > 0 && (
                    <div className="pt-1">
                      <p className="text-xs font-medium text-muted-foreground mb-1">Anomalies</p>
                      <ul className="list-disc list-inside text-xs space-y-0.5">
                        {anomalies.map((a, i) => {
                          const label = String((a as Record<string, unknown>).period_label ?? (a as Record<string, unknown>).periodLabel ?? "—");
                          return (
                            <li key={i}>
                              {label}: {formatNum(a.value)} — {a.reason ?? ""}
                              {a.explanation ? ` (${a.explanation})` : ""}
                            </li>
                          );
                        })}
                      </ul>
                    </div>
                  )}
                </div>
              );
            })}
          </div>
        )}
        {corr && (
          <div className="space-y-2">
            <h4 className="text-xs font-semibold uppercase text-muted-foreground tracking-wide">Correlations</h4>
            <div className="overflow-x-auto rounded-md border border-border">
              <table className="w-full text-sm">
                <thead><tr className="border-b border-border bg-muted/50"><th className="text-left p-2">Column A</th><th className="text-left p-2">Column B</th><th className="text-right p-2">Pearson</th><th className="text-right p-2">Spearman</th></tr></thead>
                <tbody>
                  {corr.map((c, i) => {
                    const row = c as Record<string, unknown>;
                    return (
                      <tr key={i} className={cn("border-b border-border/50 last:border-0 transition-colors", i % 2 === 0 ? "bg-transparent" : "bg-muted/15", "hover:bg-primary/5")}>
                        <td className="p-2 font-mono text-xs">{String(row.column_a ?? row.columnA ?? "—")}</td>
                        <td className="p-2 font-mono text-xs">{String(row.column_b ?? row.columnB ?? "—")}</td>
                        <td className="p-2 text-right">{row.pearson != null ? Number(row.pearson).toFixed(3) : "—"}</td>
                        <td className="p-2 text-right">{row.spearman != null ? Number(row.spearman).toFixed(3) : "—"}</td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          </div>
        )}
        {cohorts && (
          <div className="space-y-2">
            <h4 className="text-xs font-semibold uppercase text-muted-foreground tracking-wide">Cohorts</h4>
            <div className="space-y-3">
              {cohorts.map((c, i) => {
                const label = String((c as Record<string, unknown>).cohort_label ?? (c as Record<string, unknown>).cohortLabel ?? "—");
                const periods = Array.isArray(c.periods) ? c.periods : [];
                const ret = (c as Record<string, unknown>).retention_pct ?? (c as Record<string, unknown>).retentionPct;
                return (
                  <div key={i} className="rounded-md border border-border bg-muted/30 p-3 space-y-2 text-sm">
                    <div className="flex items-center justify-between">
                      <p className="font-medium">{label}</p>
                      {ret != null && typeof ret === "number" && (
                        <span className="text-xs text-muted-foreground">Retention: {formatNum(ret)}%</span>
                      )}
                    </div>
                    {periods.length > 0 && (
                      <div className="overflow-x-auto">
                        <table className="w-full min-w-[200px] text-xs">
                          <thead><tr className="border-b border-border bg-muted/50"><th className="text-left p-1.5">Period</th><th className="text-right p-1.5">Value</th></tr></thead>
                          <tbody>
                            {periods.map((p, j) => {
                              const pl = String((p as Record<string, unknown>).period_label ?? (p as Record<string, unknown>).periodLabel ?? j);
                              return (
                                <tr key={j} className={cn("border-b border-border/50 last:border-0 transition-colors", j % 2 === 0 ? "bg-transparent" : "bg-muted/15", "hover:bg-primary/5")}>
                                  <td className="p-1.5">{pl}</td>
                                  <td className="p-1.5 text-right">{formatNum(p.value)}</td>
                                </tr>
                              );
                            })}
                          </tbody>
                        </table>
                      </div>
                    )}
                  </div>
                );
              })}
            </div>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div>
      <h4 className="text-xs font-semibold uppercase text-muted-foreground tracking-wide mb-2">{title}</h4>
      <ul className="space-y-1.5">{children}</ul>
    </div>
  );
}

function Li({ children, color }: { children: React.ReactNode; color: string }) {
  return <li className="text-sm leading-relaxed flex gap-2"><span className={`${color} mt-1`}>•</span>{children}</li>;
}

function ReportList() {
  const [reports, setReports] = useState<Report[]>([]);
  const [similar, setSimilar] = useState<SimilarReportItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [connections, setConnections] = useState<ConnectionInfo[]>([]);
  const [connectionFilter, setConnectionFilter] = useState("");
  const [searchText, setSearchText] = useState("");

  const load = useCallback(async () => {
    try {
      if (searchText.trim()) {
        const res = await api.findSimilarReports(searchText.trim(), 20, connectionFilter || undefined);
        setSimilar(res.items || []);
        setReports([]);
      } else {
        const res = await api.listReports(50, 0, connectionFilter || undefined);
        setReports(res.items || []);
        setSimilar([]);
      }
    } catch {
      // Ignore: UI shows the existing (possibly empty) list on failure.
    } finally {
      setLoading(false);
    }
  }, [connectionFilter, searchText]);

  useEffect(() => { load(); }, [load]);
  useEffect(() => {
    api.listConnections().then((r) => setConnections(r.items || [])).catch(() => {});
  }, []);

  return (
    <div data-testid="reports-list-page" className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold tracking-tight">Reports</h1>
        <p className="text-muted-foreground mt-1">Generated narrative reports from your queries.</p>
      </div>
      {connections.length > 0 && (
        <div className="flex items-center gap-2">
          <label className="text-sm text-muted-foreground">Connection</label>
          <select className="h-9 rounded-md border border-input bg-background px-3 text-sm" value={connectionFilter} onChange={(e) => setConnectionFilter(e.target.value)}>
            <option value="">All</option>
            {connections.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
          </select>
        </div>
      )}
      <div className="relative">
        <Search className="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground" />
        <Input
          placeholder="Find reports like: revenue decline in west region"
          value={searchText}
          onChange={(e) => setSearchText(e.target.value)}
          className="pl-10"
        />
      </div>

      {loading ? (
        <div className="space-y-3">{[1,2,3].map(i => <Skeleton key={i} className="h-20 w-full" />)}</div>
      ) : reports.length === 0 && similar.length === 0 ? (
        <Card>
          <CardContent className="py-16 text-center space-y-4">
            <FileText className="h-10 w-10 text-muted-foreground mx-auto mb-3" />
            <p className="text-sm text-muted-foreground">No reports yet. Run a query and click Generate Report.</p>
            <Link to="/query" className={cn(buttonVariants(), "!text-white hover:!text-white dark:!text-black dark:hover:!text-black")}>Run a query</Link>
          </CardContent>
        </Card>
      ) : (
        <div data-testid="reports-list" className="space-y-3">
          {(searchText.trim() ? similar : reports).map((r) => (
            <Link key={r.id} data-testid="report-list-item" data-report-id={r.id} to={`/reports/${r.id}`}>
              <Card className="hover:border-primary/30 transition-colors cursor-pointer">
                <CardContent className="p-5 flex items-center justify-between">
                  <div className="min-w-0 flex-1">
                    <p data-testid="report-list-headline" className="text-sm font-medium">{("headline" in r && r.headline) || ("narrative" in r ? r.narrative?.headline : "") || truncate(r.sql, 60)}</p>
                    <div className="flex items-center gap-3 mt-1.5">
                      <span className="text-xs text-muted-foreground flex items-center gap-1"><Clock className="h-3 w-3" />{new Date(r.created_at).toLocaleDateString()}</span>
                      {"llm_provider" in r && <Badge variant="secondary" className="text-[10px]">{r.llm_provider}</Badge>}
                      <Badge variant="outline" className="text-[10px]">{r.connection_id}</Badge>
                      {"similarity" in r && <Badge variant="secondary" className="text-[10px]">{Math.round((r.similarity ?? 0) * 100)}% match</Badge>}
                    </div>
                  </div>
                  <FileText className="h-5 w-5 text-brand-cyan flex-shrink-0 ml-4" />
                </CardContent>
              </Card>
            </Link>
          ))}
        </div>
      )}
    </div>
  );
}

export default function Reports() {
  const { id, token } = useParams();
  return id || token ? <ReportDetail /> : <ReportList />;
}
