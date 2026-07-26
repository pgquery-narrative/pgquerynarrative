import { useState, useCallback, useEffect, useRef } from "react";
import { Card, CardHeader, CardTitle, CardContent } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { api, type RunQueryResult, type ExplainQueryResult, type Report, type ConnectionInfo, type ChatTurn, ApiError } from "@/api/client";
import { Play, FileText, AlertCircle, Clock, Rows3, Download, BarChart3, PieChart as PieChartIcon, LineChart as LineChartIcon, Table2, Sparkles, History, Lightbulb, BookmarkPlus, Binary } from "lucide-react";
import { SchemaBrowser } from "@/components/schema-browser";
import { useAnnounce } from "@/contexts/announce-context";
import { cn, formatFloat } from "@/lib/utils";
import { ResultChart, type ChartType } from "@/components/result-chart";

const QUERY_HISTORY_KEY = "pgquerynarrative_query_history";
const MAX_HISTORY = 10;

function loadHistory(): string[] {
  try {
    const raw = sessionStorage.getItem(QUERY_HISTORY_KEY);
    if (!raw) return [];
    const parsed = JSON.parse(raw) as unknown;
    if (!Array.isArray(parsed)) return [];
    return parsed.filter((x): x is string => typeof x === "string" && x.trim().length > 0).slice(0, MAX_HISTORY);
  } catch {
    return [];
  }
}

function saveHistory(items: string[]): void {
  try {
    sessionStorage.setItem(QUERY_HISTORY_KEY, JSON.stringify(items.slice(0, MAX_HISTORY)));
  } catch {
    /* ignore */
  }
}

export default function QueryRunner() {
  const [sql, setSql] = useState("");
  const [question, setQuestion] = useState("");
  const [limit, setLimit] = useState("100");
  const [result, setResult] = useState<RunQueryResult | null>(null);
  const [explainResult, setExplainResult] = useState<ExplainQueryResult | null>(null);
  const [report, setReport] = useState<Report | null>(null);
  const [loading, setLoading] = useState(false);
  const [explainLoading, setExplainLoading] = useState(false);
  const [explainAnalyze, setExplainAnalyze] = useState(false);
  const [showPlanJson, setShowPlanJson] = useState(false);
  const [genLoading, setGenLoading] = useState(false);
  const [askLoading, setAskLoading] = useState(false);
  const [error, setError] = useState("");
  const [chartType, setChartType] = useState<ChartType | null>(null);
  const [history, setHistory] = useState<string[]>(loadHistory);
  const [suggestions, setSuggestions] = useState<{ sql: string; title: string; source: string }[]>([]);
  const [suggestionsLoading, setSuggestionsLoading] = useState(true);
  const [questionSuggestions, setQuestionSuggestions] = useState<string[]>([]);
  const [chatSessionId, setChatSessionId] = useState("");
  const [chatTurns, setChatTurns] = useState<ChatTurn[]>([]);
  const [chatFollowUps, setChatFollowUps] = useState<string[]>([]);
  const [saveName, setSaveName] = useState("");
  const [saveTags, setSaveTags] = useState("");
  const [saveLoading, setSaveLoading] = useState(false);
  const [saveSuccess, setSaveSuccess] = useState("");
  const [connections, setConnections] = useState<ConnectionInfo[]>([]);
  const [connectionId, setConnectionId] = useState<string>("");
  const resultsRef = useRef<HTMLDivElement>(null);
  const editorRef = useRef<HTMLTextAreaElement>(null);
  const announce = useAnnounce();

  useEffect(() => {
    saveHistory(history);
  }, [history]);

  useEffect(() => {
    api.listConnections().then((r) => {
      const items = r.items ?? [];
      setConnections(items);
      setConnectionId((prev) => prev || items[0]?.id || "");
    }).catch(() => {});
  }, []);

  useEffect(() => {
    api.getSuggestions(6).then((r) => setSuggestions(r.suggestions ?? [])).catch(() => {}).finally(() => setSuggestionsLoading(false));
  }, []);

  useEffect(() => {
    api.getSuggestedQuestions(8, connectionId || undefined).then((r) => setQuestionSuggestions(r.questions ?? [])).catch(() => {});
  }, [connectionId]);

  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key === "e") {
        e.preventDefault();
        editorRef.current?.focus();
      }
    };
    window.addEventListener("keydown", handler);
    return () => window.removeEventListener("keydown", handler);
  }, []);

  useEffect(() => {
    if ((result && !loading) || (explainResult && !explainLoading)) {
      resultsRef.current?.scrollIntoView({ behavior: "smooth", block: "start" });
    }
  }, [result, loading, explainResult, explainLoading]);

  const runQuery = useCallback(async (sqlOverride?: string) => {
    const toRun = (sqlOverride ?? sql).trim();
    if (!toRun) {
      setError("SQL query cannot be empty.");
      return;
    }
    setError("");
    setLoading(true);
    setResult(null);
    setExplainResult(null);
    setReport(null);
    setChartType(null);
    if (sqlOverride) setSql(toRun);
    try {
      const r = await api.runQuery(toRun, Math.max(1, Math.min(1000, Number(limit) || 100)), connectionId || undefined);
      setResult(r);
      setHistory((prev) => {
        const rest = prev.filter((h) => h.trim() !== toRun);
        return [toRun, ...rest];
      });
      announce(`Query completed. ${r.row_count ?? 0} rows returned.`, "polite");
    } catch (e) {
      const msg = e instanceof ApiError ? e.message : "Query failed";
      setError(msg);
      announce(`Error: ${msg}`, "assertive");
    } finally {
      setLoading(false);
    }
  }, [sql, limit, announce, connectionId]);

  const explainQuery = useCallback(async () => {
    const toExplain = sql.trim();
    if (!toExplain) {
      setError("SQL query cannot be empty.");
      return;
    }
    setError("");
    setExplainLoading(true);
    setExplainResult(null);
    setShowPlanJson(false);
    try {
      const r = await api.explainQuery(toExplain, explainAnalyze, connectionId || undefined);
      setExplainResult(r);
      setHistory((prev) => {
        const rest = prev.filter((h) => h.trim() !== toExplain);
        return [toExplain, ...rest];
      });
      const n = r.findings?.length ?? 0;
      announce(
        `Explain completed. Total cost ${formatFloat(r.total_cost)}. ${n} finding${n === 1 ? "" : "s"}.`,
        "polite"
      );
    } catch (e) {
      const msg = e instanceof ApiError ? e.message : "Explain failed";
      setError(msg);
      announce(`Error: ${msg}`, "assertive");
    } finally {
      setExplainLoading(false);
    }
  }, [sql, explainAnalyze, announce, connectionId]);

  const generateReport = useCallback(async () => {
    if (!sql.trim()) return;
    setGenLoading(true);
    try {
      const r = await api.generateReport(sql, connectionId || undefined);
      setReport(r);
      announce("Report generated.", "polite");
    } catch (e) {
      const msg = e instanceof ApiError
        ? (e.status === 500 ? `${e.message} Check that the LLM is running (e.g. Ollama) and configured.` : e.message)
        : "Report generation failed.";
      setError(msg);
      announce(`Error: ${msg}`, "assertive");
    } finally {
      setGenLoading(false);
    }
  }, [sql, announce, connectionId]);

  const saveQuery = useCallback(async () => {
    const normalizedSQL = sql.trim().replace(/;\s*$/, "");
    if (!normalizedSQL) {
      setError("Write a SQL query before saving.");
      return;
    }
    if (!saveName.trim()) {
      setError("Enter a name for the saved query.");
      return;
    }

    const tags = saveTags
      .split(",")
      .map((t) => t.trim())
      .filter((t) => t.length > 0);

    setError("");
    setSaveSuccess("");
    setSaveLoading(true);
    try {
      await api.saveQuery(saveName.trim(), normalizedSQL, tags, connectionId || undefined);
      setSaveSuccess(`Saved "${saveName.trim()}" to Saved Queries.`);
      setSaveName("");
      setSaveTags("");
    } catch (e) {
      setError(e instanceof ApiError ? e.message : "Failed to save query.");
    } finally {
      setSaveLoading(false);
    }
  }, [sql, saveName, saveTags, connectionId]);

  const ask = useCallback(async (questionOverride?: string) => {
    const text = (questionOverride ?? question).trim();
    if (!text) {
      setError("Enter a question.");
      return;
    }
    setError("");
    setAskLoading(true);
    setResult(null);
    setExplainResult(null);
    setReport(null);
    setChartType(null);
    try {
      const r = await api.askChat(text, chatSessionId || undefined, connectionId || undefined);
      setSql(r.sql);
      setReport(r.report);
      setQuestion(text);
      setChatSessionId(r.session_id || chatSessionId);
      setChatTurns(r.history || []);
      setChatFollowUps(r.follow_ups || []);
      announce("Answer and report generated.", "polite");
    } catch (e) {
      const msg = e instanceof ApiError
        ? (e.status === 500 ? `${e.message} Check that the LLM is running (e.g. Ollama) and configured.` : e.message)
        : "Ask failed. Check the console for details.";
      setError(msg);
      announce(`Error: ${msg}`, "assertive");
    } finally {
      setAskLoading(false);
    }
  }, [question, announce, connectionId]);

  const insertIntoEditor = useCallback((text: string) => {
    setSql((prev) => {
      const sep = prev && !prev.endsWith(" ") && !prev.endsWith("\n") ? " " : "";
      return prev + sep + text;
    });
  }, []);

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold tracking-tight">Query Runner</h1>
        <p className="text-muted-foreground mt-1">Run read-only SQL, visualize results, and save reusable queries.</p>
      </div>

      <div className="grid grid-cols-1 xl:grid-cols-[260px_1fr] gap-4 xl:gap-6">
        <aside className="xl:sticky xl:top-4 xl:self-start shrink-0">
          <h2 className="text-sm font-semibold text-muted-foreground mb-2 px-1">Database schema</h2>
          <SchemaBrowser onInsert={insertIntoEditor} connectionId={connectionId || undefined} />
        </aside>
        <div className="space-y-6 min-w-0">
      {/* Ask in natural language */}
      <Card className="border-primary/10">
        <CardHeader>
          <CardTitle className="text-sm flex items-center gap-2">
            <Sparkles className="h-4 w-4 text-primary" />
            Ask in natural language
          </CardTitle>
          <p className="text-xs text-muted-foreground mt-1">Describe what you want in plain English; the app will generate SQL and a narrative report.</p>
        </CardHeader>
        <CardContent className="flex flex-wrap gap-3">
          {connections.length > 0 && (
            <select className="h-10 rounded-md border border-input bg-background px-3 text-sm" value={connectionId} onChange={(e) => setConnectionId(e.target.value)}>
              {connections.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
            </select>
          )}
          <Input
            placeholder="e.g. What were the top 5 products by revenue?"
            value={question}
            onChange={(e) => setQuestion(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && ask()}
            className="flex-1 min-w-[240px]"
            disabled={askLoading}
          />
          <Button onClick={() => { void ask(); }} disabled={askLoading}>
            {askLoading ? <span className="animate-spin h-4 w-4 border-2 border-current border-t-transparent rounded-full" /> : <Sparkles className="h-4 w-4" />}
            Ask
          </Button>
        </CardContent>
        {questionSuggestions.length > 0 && (
          <CardContent className="pt-0">
            <div className="flex flex-wrap gap-2">
              {questionSuggestions.map((q, i) => (
                <Button
                  key={i}
                  variant="outline"
                  size="sm"
                  onClick={() => { void ask(q); }}
                  className="text-left font-normal h-auto py-2 px-3 whitespace-normal"
                >
                  <span className="truncate max-w-[340px] block">{q}</span>
                </Button>
              ))}
            </div>
          </CardContent>
        )}
      </Card>

      {/* Ask conversation history */}
      {(chatTurns.length > 0 || chatFollowUps.length > 0) && (
        <Card>
          <CardHeader>
            <CardTitle className="text-sm">Ask conversation</CardTitle>
          </CardHeader>
          <CardContent className="space-y-3">
            {chatTurns.length > 0 && (
              <div className="space-y-2">
                {chatTurns.map((t, i) => (
                  <div key={i} className="rounded-md border border-border p-2 text-xs">
                    <p className="font-medium">Q: {t.question}</p>
                    <p className="text-muted-foreground font-mono mt-1 truncate">SQL: {t.sql}</p>
                  </div>
                ))}
              </div>
            )}
            {chatFollowUps.length > 0 && (
              <div className="flex flex-wrap gap-2">
                {chatFollowUps.map((f, i) => (
                  <Button key={i} variant="outline" size="sm" onClick={() => { void ask(f); }}>
                    {f}
                  </Button>
                ))}
              </div>
            )}
          </CardContent>
        </Card>
      )}

      {/* Query suggestions */}
      {(suggestionsLoading || suggestions.length > 0) && (
        <Card>
          <CardHeader>
            <CardTitle className="text-sm flex items-center gap-2">
              <Lightbulb className="h-4 w-4 text-primary" />
              Suggested queries
            </CardTitle>
            <p className="text-xs text-muted-foreground mt-1">Click to load and run.</p>
          </CardHeader>
          <CardContent>
            {suggestionsLoading ? (
              <div className="flex flex-wrap gap-2">
                {[1, 2, 3].map((i) => <Skeleton key={i} className="h-9 w-48" />)}
              </div>
            ) : (
              <div className="flex flex-wrap gap-2">
                {suggestions.map((s, i) => (
                  <Button
                    key={i}
                    variant="outline"
                    size="sm"
                    onClick={() => { setSql(s.sql); runQuery(s.sql); }}
                    className="text-left font-normal h-auto py-2 px-3 whitespace-normal"
                    title={s.source}
                  >
                    <span className="truncate max-w-[200px] block">{s.title}</span>
                  </Button>
                ))}
              </div>
            )}
          </CardContent>
        </Card>
      )}

      {/* Editor container — glass panel with top + corner accents */}
      <Card className="panel-accent-top panel-corner-accent">
        <CardHeader className="pb-2">
          <CardTitle className="text-sm">Compose SQL</CardTitle>
          <p className="text-xs text-muted-foreground">Use schema click-insert and keep queries in allowed schemas only.</p>
        </CardHeader>
        <CardContent className="p-6 space-y-4">
          <Textarea
            ref={editorRef}
            data-testid="query-sql"
            placeholder="SELECT product_category, SUM(total_amount) AS total&#10;FROM demo.sales&#10;GROUP BY product_category"
            value={sql}
            onChange={(e) => setSql(e.target.value)}
            className="min-h-[160px] font-mono text-sm"
            onKeyDown={(e) => { if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) runQuery(); }}
          />
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div className="flex flex-wrap items-center gap-3">
              <Button data-testid="query-run" onClick={() => runQuery()} disabled={loading || explainLoading}>
                {loading ? <span className="animate-spin h-4 w-4 border-2 border-current border-t-transparent rounded-full" /> : <Play className="h-4 w-4" />}
                Run Query
              </Button>
              <Button variant="outline" onClick={() => { void explainQuery(); }} disabled={explainLoading || loading || !sql.trim()}>
                {explainLoading ? <span className="animate-spin h-4 w-4 border-2 border-current border-t-transparent rounded-full" /> : <Binary className="h-4 w-4" />}
                Explain
              </Button>
              <Button data-testid="query-generate-report" variant="secondary" onClick={generateReport} disabled={genLoading || !sql.trim()}>
                {genLoading ? <span className="animate-spin h-4 w-4 border-2 border-current border-t-transparent rounded-full" /> : <FileText className="h-4 w-4" />}
                Generate Report
              </Button>
              <label className="flex items-center gap-2 text-xs text-muted-foreground cursor-pointer select-none">
                <input
                  type="checkbox"
                  className="rounded border-input"
                  checked={explainAnalyze}
                  onChange={(e) => setExplainAnalyze(e.target.checked)}
                />
                ANALYZE
              </label>
            </div>
            <div className="flex items-center gap-2">
              <label className="text-xs text-muted-foreground whitespace-nowrap">Limit</label>
              <Input type="number" value={limit} onChange={(e) => setLimit(e.target.value)} className="w-20 h-10 text-xs" min={1} max={1000} />
            </div>
          </div>
          <div className="grid grid-cols-1 md:grid-cols-[minmax(220px,1fr)_minmax(220px,1fr)_auto] gap-2">
            <Input
              data-testid="query-save-name"
              aria-label="Saved query name"
              placeholder="Saved query name"
              value={saveName}
              onChange={(e) => setSaveName(e.target.value)}
              maxLength={200}
              className="h-10"
            />
            <Input
              aria-label="Optional saved query tags"
              placeholder="Tags (optional, comma-separated)"
              value={saveTags}
              onChange={(e) => setSaveTags(e.target.value)}
              className="h-10"
            />
            <Button data-testid="query-save" variant="outline" onClick={saveQuery} disabled={saveLoading || !sql.trim()}>
              {saveLoading ? <span className="animate-spin h-4 w-4 border-2 border-current border-t-transparent rounded-full" /> : <BookmarkPlus className="h-4 w-4" />}
              Save Query
            </Button>
          </div>
          {saveSuccess && <p data-testid="query-save-success" className="text-xs text-emerald-600">{saveSuccess}</p>}
          <p className="text-[11px] text-muted-foreground">Tip: Save stable, business-friendly queries so teammates can reuse them from Saved Queries.</p>
          <p className="text-[11px] text-muted-foreground">Ctrl+Enter run. Ctrl+E focus editor. Explain shows the Postgres plan (check ANALYZE to execute). Click schema items to insert. Only SELECT/WITH on allowed schemas.</p>
        </CardContent>
      </Card>

      {/* Query history */}
      {history.length > 0 && (
        <Card>
          <CardHeader>
            <CardTitle className="text-sm flex items-center gap-2">
              <History className="h-4 w-4" />
              Recent queries
            </CardTitle>
            <p className="text-xs text-muted-foreground mt-1">Click to re-run (last {MAX_HISTORY} in this session).</p>
          </CardHeader>
          <CardContent>
            <div className="flex flex-col gap-1.5">
              {history.map((q, i) => (
                <button
                  key={i}
                  type="button"
                  onClick={() => runQuery(q)}
                  disabled={loading}
                  className="text-left px-3 py-2 rounded-md bg-secondary/50 hover:bg-secondary text-xs font-mono truncate max-w-full transition-colors"
                  title={q}
                >
                  {q.length > 80 ? q.slice(0, 77) + "..." : q}
                </button>
              ))}
            </div>
          </CardContent>
        </Card>
      )}

      {/* Error: prominent alert with border and ring so it stands out */}
      {error && (
        <div
          role="alert"
          className="flex items-start gap-3 p-4 rounded-lg border border-destructive/40 bg-destructive/10 text-destructive text-sm shadow-sm ring-1 ring-destructive/20"
        >
          <AlertCircle className="h-5 w-5 flex-shrink-0 mt-0.5" aria-hidden />
          <div className="min-w-0">{error}</div>
        </div>
      )}

      {/* Loading skeleton with accent */}
      {(loading || explainLoading) && (
        <Card className="panel-accent-top">
          <CardContent className="p-6 space-y-3">
            <Skeleton className="h-6 w-48" />
            <Skeleton className="h-48 w-full" />
          </CardContent>
        </Card>
      )}

      {/* EXPLAIN plan findings + query results share one scroll target */}
      {((explainResult && !explainLoading) || (result && !loading)) && (
        <div ref={resultsRef} className="space-y-6">
      {explainResult && !explainLoading && (
          <Card className="panel-accent-top panel-corner-accent border-primary/20">
            <CardHeader className="flex flex-row flex-wrap items-center justify-between gap-3">
              <div>
                <CardTitle className="text-base">Query plan</CardTitle>
                <p className="text-xs text-muted-foreground mt-1 font-mono truncate max-w-[48rem]" title={explainResult.sql}>
                  {explainResult.sql}
                </p>
              </div>
              <div className="flex items-center gap-3 text-xs text-foreground/85">
                <span>Total cost {formatFloat(explainResult.total_cost)}</span>
                <span className="flex items-center gap-1"><Clock className="h-3 w-3" />{explainResult.execution_time_ms}ms</span>
                <span>{explainResult.findings?.length ?? 0} findings</span>
              </div>
            </CardHeader>
            <CardContent className="space-y-4">
              {(explainResult.findings ?? []).length === 0 ? (
                <p className="text-sm text-muted-foreground">No seq-scan or high-cost findings. Plan looks fine for the heuristics we check.</p>
              ) : (
                <ul className="space-y-2">
                  {(explainResult.findings ?? []).map((f, i) => (
                    <li
                      key={i}
                      className={cn(
                        "rounded-md border px-3 py-2 text-sm",
                        f.is_seq_scan ? "border-amber-500/40 bg-amber-500/10" : "border-border bg-secondary/40"
                      )}
                    >
                      <div className="flex flex-wrap items-center gap-2 text-xs font-semibold uppercase tracking-wide text-muted-foreground mb-1">
                        <span>{f.node_type}</span>
                        {f.is_seq_scan && <span className="text-amber-600 dark:text-amber-400">Seq Scan</span>}
                        {(f.schema || f.relation) && (
                          <span className="font-mono normal-case tracking-normal text-foreground/80">
                            {[f.schema, f.relation].filter(Boolean).join(".")}
                          </span>
                        )}
                        {f.estimated_cost != null && (
                          <span className="normal-case tracking-normal font-normal">cost {formatFloat(f.estimated_cost)}</span>
                        )}
                      </div>
                      <p className="text-sm leading-relaxed text-foreground/95">{f.message}</p>
                    </li>
                  ))}
                </ul>
              )}
              <div>
                <Button variant="ghost" size="sm" onClick={() => setShowPlanJson((v) => !v)}>
                  {showPlanJson ? "Hide raw plan JSON" : "Show raw plan JSON"}
                </Button>
                {showPlanJson && (
                  <pre className="mt-2 max-h-[360px] overflow-auto rounded-md border border-border bg-background/60 p-3 text-[11px] font-mono leading-relaxed">
                    {JSON.stringify(explainResult.plan, null, 2)}
                  </pre>
                )}
              </div>
            </CardContent>
          </Card>
      )}

      {/* Results: chart view or table */}
      {result && !loading && (
        <div className="space-y-6">
          {/* Suggested charts: click to visualize */}
          {result.chart_suggestions && result.chart_suggestions.length > 0 && (
            <Card>
              <CardHeader>
                <CardTitle className="text-sm">Suggested Charts</CardTitle>
                <p className="text-xs text-muted-foreground mt-1">Click a chart type to visualize the result.</p>
              </CardHeader>
              <CardContent className="space-y-4">
                <div className="flex flex-wrap gap-2">
                  <Button
                    variant={chartType === "table" || chartType === null ? "default" : "outline"}
                    size="sm"
                    onClick={() => setChartType("table")}
                    className="gap-1.5"
                  >
                    <Table2 className="h-3.5 w-3.5" /> Table
                  </Button>
                  {result.chart_suggestions.map((s, i) => {
                    const type = (s.chart_type?.toLowerCase() || "") as ChartType;
                    if (type !== "bar" && type !== "line" && type !== "pie") return null;
                    return (
                      <Button
                        key={i}
                        variant={chartType === type ? "default" : "outline"}
                        size="sm"
                        onClick={() => setChartType(type)}
                        className="gap-1.5"
                        title={s.reason}
                      >
                        {type === "bar" && <BarChart3 className="h-3.5 w-3.5" />}
                        {type === "line" && <LineChartIcon className="h-3.5 w-3.5" />}
                        {type === "pie" && <PieChartIcon className="h-3.5 w-3.5" />}
                        {s.label}
                      </Button>
                    );
                  })}
                </div>
              </CardContent>
            </Card>
          )}

          <Card data-testid="query-results" className="panel-accent-top panel-corner-accent border-primary/20">
            <CardHeader className="flex flex-row items-center justify-between">
              <CardTitle className="text-base">Results</CardTitle>
              <div className="flex items-center gap-3 text-xs text-foreground/85">
                <span data-testid="query-row-count" className="flex items-center gap-1"><Rows3 className="h-3 w-3" />{result.row_count} rows</span>
                <span className="flex items-center gap-1"><Clock className="h-3 w-3" />{result.execution_time_ms}ms</span>
              </div>
            </CardHeader>
            <CardContent className="p-0">
              {(result.rows ?? []).length === 0 ? (
                <p className="text-sm text-muted-foreground text-center py-10">No rows returned.</p>
              ) : (!result.chart_suggestions?.length || chartType === "table" || chartType === null) ? (
                <div className="overflow-auto max-h-[460px] rounded-b-lg border-t border-border/70 bg-background/45">
                  <table className="w-full text-[13px] leading-relaxed">
                    <thead className="sticky top-0 bg-secondary/95 border-b border-border z-[1] backdrop-blur">
                      <tr>
                        {(result.columns ?? []).map((c, i) => (
                          <th
                            key={i}
                            className="text-left text-[11px] uppercase tracking-wide font-semibold text-foreground/90 px-4 py-3.5 whitespace-nowrap"
                          >
                            {c.name}
                          </th>
                        ))}
                      </tr>
                    </thead>
                    <tbody>
                      {(result.rows ?? []).map((row, ri) => (
                        <tr
                          key={ri}
                          className={cn(
                            "border-b border-border/60 transition-colors",
                            ri % 2 === 0 ? "bg-background/10" : "bg-muted/35",
                            "hover:bg-primary/12"
                          )}
                        >
                          {row.map((cell, ci) => (
                            <td
                              key={ci}
                              className={cn(
                                "px-4 py-2.5 whitespace-nowrap font-mono text-[12px] text-foreground/95",
                                ci === 0 && "font-medium text-foreground"
                              )}
                            >
                              {cell == null ? (
                                <span className="text-muted-foreground/80">NULL</span>
                              ) : typeof cell === "number" ? (
                                formatFloat(cell)
                              ) : (
                                String(cell)
                              )}
                            </td>
                          ))}
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              ) : (
                <div className="p-4">
                  <ResultChart chartType={chartType!} columns={result.columns} rows={result.rows} />
                </div>
              )}
            </CardContent>
          </Card>
        </div>
      )}
        </div>
      )}

      {/* Report narrative — HUD accent: small top border gradient */}
      {report && (
        <Card className="border-primary/20 panel-accent-top">
          <CardHeader className="flex flex-row items-center justify-between">
            <div>
              <CardTitle className="text-brand-cyan">Narrative Report</CardTitle>
              <p className="text-xs text-muted-foreground mt-1">{report.llm_provider} / {report.llm_model}</p>
            </div>
            <div className="flex gap-2">
              <a href={`/reports/${report.id}`}>
                <Button data-testid="query-report-open" variant="outline" size="sm">Open report</Button>
              </a>
              <a href={`/web/reports/export/pdf?id=${report.id}`} download>
                <Button variant="ghost" size="sm"><Download className="h-4 w-4" /> PDF</Button>
              </a>
            </div>
          </CardHeader>
          <CardContent className="space-y-4">
            {report.narrative.headline && <h3 data-testid="query-report-headline" className="text-lg font-semibold">{report.narrative.headline}</h3>}
            {report.narrative.takeaways && report.narrative.takeaways.length > 0 && (
              <div>
                <h4 className="text-xs font-semibold uppercase text-muted-foreground tracking-wide mb-2">Key Takeaways</h4>
                <ul className="space-y-1.5">{report.narrative.takeaways.map((t, i) => <li key={i} className="text-sm leading-relaxed flex gap-2"><span className="text-primary mt-1">•</span>{t}</li>)}</ul>
              </div>
            )}
            {report.narrative.drivers && report.narrative.drivers.length > 0 && (
              <div>
                <h4 className="text-xs font-semibold uppercase text-muted-foreground tracking-wide mb-2">Drivers</h4>
                <ul className="space-y-1.5">{report.narrative.drivers.map((d, i) => <li key={i} className="text-sm leading-relaxed flex gap-2"><span className="text-brand-blue mt-1">•</span>{d}</li>)}</ul>
              </div>
            )}
            {report.narrative.recommendations && report.narrative.recommendations.length > 0 && (
              <div>
                <h4 className="text-xs font-semibold uppercase text-muted-foreground tracking-wide mb-2">Recommendations</h4>
                <ul className="space-y-1.5">{report.narrative.recommendations.map((r, i) => <li key={i} className="text-sm leading-relaxed flex gap-2"><span className="text-accent mt-1">•</span>{r}</li>)}</ul>
              </div>
            )}
          </CardContent>
        </Card>
      )}
        </div>
      </div>
    </div>
  );
}
