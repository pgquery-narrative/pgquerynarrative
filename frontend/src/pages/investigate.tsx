import { useCallback, useEffect, useState } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { TrustBar } from "@/components/trust-bar";
import { PlanExplorer } from "@/components/plan-explorer";
import { PlanCompare } from "@/components/plan-compare";
import { api, type Investigation, type RankedCandidate, ApiError } from "@/api/client";
import {
  Search, FileText, GitCompare, CheckCircle2, ArrowRight, Play, Loader2, Sparkles, ListOrdered,
} from "lucide-react";
import { cn, formatFloat, truncate } from "@/lib/utils";

const STEPS = [
  { id: "select", label: "Find query" },
  { id: "inspect", label: "Inspect SQL" },
  { id: "explain", label: "EXPLAIN evidence" },
  { id: "compare", label: "Compare improvement" },
  { id: "verify", label: "Verify equivalence" },
  { id: "report", label: "Generate report" },
];

export default function InvestigatePage() {
  const { id } = useParams();
  const [searchParams] = useSearchParams();
  const navigate = useNavigate();
  const [investigation, setInvestigation] = useState<Investigation | null>(null);
  const [loading, setLoading] = useState(!!id);
  const [error, setError] = useState("");
  const [candidateSql, setCandidateSql] = useState("");
  const [suggestRationale, setSuggestRationale] = useState("");
  const [rankedCandidates, setRankedCandidates] = useState<RankedCandidate[]>([]);
  const [actionLoading, setActionLoading] = useState("");

  const load = useCallback(async (investigationId: string, candidateHint?: string) => {
    setLoading(true);
    setError("");
    try {
      const inv = await api.getInvestigation(investigationId);
      setInvestigation(inv);
      if (inv.candidate_sql) setCandidateSql(inv.candidate_sql);
      else if (candidateHint) setCandidateSql(candidateHint);
    } catch (e) {
      setError(e instanceof ApiError ? e.message : "Failed to load investigation");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    if (id) {
      setActionLoading("");
      void load(id, searchParams.get("candidate") || undefined);
      return;
    }
    const sql = searchParams.get("sql");
    const title = searchParams.get("title") || "Query Investigation";
    if (sql) {
      setActionLoading("create");
      const calls = searchParams.get("calls");
      const meanTime = searchParams.get("mean_time_ms");
      const totalTime = searchParams.get("total_time_ms");
      const rows = searchParams.get("rows");
      api.createInvestigation({
        title,
        sql,
        ...(calls ? { calls: Number(calls) } : {}),
        ...(meanTime ? { mean_time_ms: Number(meanTime) } : {}),
        ...(totalTime ? { total_time_ms: Number(totalTime) } : {}),
        ...(rows ? { rows: Number(rows) } : {}),
      })
        .then((inv) => {
          setActionLoading("");
          const candidate = searchParams.get("candidate");
          const next = candidate
            ? `/investigate/${inv.id}?candidate=${encodeURIComponent(candidate)}`
            : `/investigate/${inv.id}`;
          navigate(next, { replace: true });
        })
        .catch((e) => {
          setError(e instanceof ApiError ? e.message : "Failed to create investigation");
          setActionLoading("");
        });
    }
  }, [id, searchParams, load, navigate]);

  const addCandidate = async () => {
    if (!investigation || !candidateSql.trim()) return;
    setActionLoading("candidate");
    setError("");
    try {
      const inv = await api.addInvestigationCandidate(investigation.id, candidateSql.trim());
      setInvestigation(inv);
    } catch (e) {
      setError(e instanceof ApiError ? e.message : "Failed to compare plans");
    } finally {
      setActionLoading("");
    }
  };

  const suggestRewrite = async () => {
    if (!investigation) return;
    setActionLoading("suggest");
    setError("");
    try {
      const res = await api.suggestInvestigationRewrite(investigation.id);
      const top = res.candidates?.[0];
      if (!top?.sql) {
        setError("No rewrite suggestions for this query yet. Supported pattern: DATE_TRUNC equality on a column.");
        setSuggestRationale("");
        return;
      }
      setCandidateSql(top.sql);
      setSuggestRationale(top.rationale || "");
    } catch (e) {
      setError(e instanceof ApiError ? e.message : "Failed to suggest rewrite");
    } finally {
      setActionLoading("");
    }
  };

  const rankCandidates = async () => {
    if (!investigation) return;
    setActionLoading("rank");
    setError("");
    try {
      const res = await api.rankInvestigationCandidates(investigation.id);
      const items = res.candidates ?? [];
      setRankedCandidates(items);
      const topRewrite = items.find((c) => c.rankable && c.sql);
      if (topRewrite?.sql) {
        setCandidateSql(topRewrite.sql);
        setSuggestRationale(topRewrite.rationale || "");
      } else if (items.length === 0) {
        setError("No ranked candidates for this query yet.");
      }
    } catch (e) {
      setError(e instanceof ApiError ? e.message : "Failed to rank candidates");
    } finally {
      setActionLoading("");
    }
  };

  const generateReport = async () => {
    if (!investigation) return;
    setActionLoading("report");
    setError("");
    try {
      const inv = await api.generateInvestigationReport(investigation.id);
      setInvestigation(inv);
      if (inv.report_id) {
        navigate(`/reports/${inv.report_id}`);
        return;
      }
    } catch (e) {
      setError(e instanceof ApiError ? e.message : "Failed to generate report");
    } finally {
      setActionLoading("");
    }
  };

  const currentStep = investigation?.comparison
    ? investigation.report_id ? 5 : 3
    : investigation?.explain ? 2 : 0;

  if (!id && !searchParams.get("sql")) {
    return <InvestigateLanding />;
  }

  if (loading || actionLoading === "create") {
    return (
      <div className="space-y-6">
        <Skeleton className="h-8 w-64" />
        <Skeleton className="h-24 w-full" />
        <Skeleton className="h-64 w-full" />
      </div>
    );
  }

  if (!investigation) {
    return (
      <div className="text-center py-12">
        <p className="text-muted-foreground">{error || "Investigation not found."}</p>
        <Link to="/investigate" className="text-primary text-sm mt-2 inline-block">Start new investigation</Link>
      </div>
    );
  }

  return (
    <div className="space-y-6">
      <div className="flex flex-col md:flex-row md:items-start md:justify-between gap-4">
        <div>
          <div className="flex items-center gap-2 mb-1">
            <Badge variant="outline" className="text-[10px]">Query Investigation</Badge>
            <Badge variant={investigation.status === "complete" ? "success" : "secondary"} className="text-[10px] capitalize">
              {investigation.status}
            </Badge>
          </div>
          <h1 className="text-2xl font-bold tracking-tight">{investigation.title}</h1>
          <p className="text-muted-foreground mt-1 text-sm">
            Evidence-backed PostgreSQL query investigation workflow
          </p>
        </div>
        {investigation.report_id && (
          <Link to={`/reports/${investigation.report_id}`}>
            <Button variant="secondary"><FileText className="h-4 w-4" /> View report</Button>
          </Link>
        )}
      </div>

      <TrustBar />

      {/* Workflow steps */}
      <div className="flex flex-wrap gap-2">
        {STEPS.map((step, i) => (
          <div
            key={step.id}
            className={cn(
              "flex items-center gap-1.5 text-xs px-3 py-1.5 rounded-full border",
              i <= currentStep
                ? "border-primary/30 bg-primary/10 text-primary"
                : "border-border text-muted-foreground"
            )}
          >
            {i < currentStep ? <CheckCircle2 className="h-3 w-3" /> : <span className="w-3 text-center">{i + 1}</span>}
            {step.label}
            {i < STEPS.length - 1 && <ArrowRight className="h-3 w-3 opacity-40 ml-1 hidden sm:block" />}
          </div>
        ))}
      </div>

      {error && (
        <div className="rounded-lg border border-destructive/30 bg-destructive/5 p-4 text-sm text-destructive">
          {error}
        </div>
      )}

      {/* Stats snapshot */}
      {investigation.stat_snapshot && (
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-sm">pg_stat_statements snapshot</CardTitle>
          </CardHeader>
          <CardContent className="grid grid-cols-2 md:grid-cols-4 gap-4 text-sm">
            {investigation.stat_snapshot.calls != null && (
              <Stat label="Calls" value={investigation.stat_snapshot.calls.toLocaleString()} />
            )}
            {investigation.stat_snapshot.mean_time_ms != null && (
              <Stat label="Mean time" value={`${formatFloat(investigation.stat_snapshot.mean_time_ms)}ms`} />
            )}
            {investigation.stat_snapshot.total_time_ms != null && (
              <Stat label="Total time" value={`${formatFloat(investigation.stat_snapshot.total_time_ms)}ms`} />
            )}
            {investigation.stat_snapshot.rows != null && (
              <Stat label="Rows" value={investigation.stat_snapshot.rows.toLocaleString()} />
            )}
          </CardContent>
        </Card>
      )}

      {/* Source SQL */}
      <Card>
        <CardHeader>
          <CardTitle className="text-sm flex items-center gap-2">
            <Search className="h-4 w-4" /> Source query
          </CardTitle>
          {investigation.query_fingerprint && (
            <CardDescription>Fingerprint: {investigation.query_fingerprint}</CardDescription>
          )}
        </CardHeader>
        <CardContent>
          <pre className="text-xs font-mono bg-muted/40 rounded-lg p-4 overflow-x-auto whitespace-pre-wrap">{investigation.sql}</pre>
        </CardContent>
      </Card>

      {/* Plan explorer */}
      {investigation.explain && (
        <Card>
          <CardHeader>
            <CardTitle className="text-sm">Execution plan</CardTitle>
            <CardDescription>
              Total cost {formatFloat(investigation.explain.total_cost)} · {investigation.explain.findings?.length ?? 0} findings
            </CardDescription>
          </CardHeader>
          <CardContent>
            <PlanExplorer plan={investigation.explain.plan} findings={investigation.explain.findings} />
          </CardContent>
        </Card>
      )}

      {/* Candidate comparison */}
      <Card>
        <CardHeader>
          <CardTitle className="text-sm flex items-center gap-2">
            <GitCompare className="h-4 w-4" /> Candidate improvement
          </CardTitle>
          <CardDescription>Propose a rewrite and compare execution plans without modifying production.</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <Textarea
            value={candidateSql}
            onChange={(e) => {
              setCandidateSql(e.target.value);
              setSuggestRationale("");
            }}
            placeholder="Paste candidate SQL rewrite, or click Suggest rewrite…"
            className="font-mono text-xs min-h-[100px]"
          />
          {suggestRationale && (
            <p className="text-xs text-muted-foreground">{suggestRationale}</p>
          )}
          <div className="flex flex-wrap gap-2">
            <Button
              variant="outline"
              onClick={() => void suggestRewrite()}
              disabled={actionLoading === "suggest" || actionLoading === "candidate" || actionLoading === "rank"}
            >
              {actionLoading === "suggest" ? <Loader2 className="h-4 w-4 animate-spin" /> : <Sparkles className="h-4 w-4" />}
              Suggest rewrite
            </Button>
            <Button
              variant="outline"
              onClick={() => void rankCandidates()}
              disabled={actionLoading === "rank" || actionLoading === "candidate" || actionLoading === "suggest"}
            >
              {actionLoading === "rank" ? <Loader2 className="h-4 w-4 animate-spin" /> : <ListOrdered className="h-4 w-4" />}
              Rank candidates
            </Button>
            <Button onClick={() => void addCandidate()} disabled={!candidateSql.trim() || actionLoading === "candidate"}>
              {actionLoading === "candidate" ? <Loader2 className="h-4 w-4 animate-spin" /> : <GitCompare className="h-4 w-4" />}
              Compare plans
            </Button>
          </div>
          {rankedCandidates.length > 0 && (
            <div className="space-y-2">
              <p className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">Ranked candidates</p>
              {rankedCandidates.map((c, i) => (
                <div key={i} className="rounded-md border border-border/50 p-3 space-y-2 text-xs">
                  <div className="flex flex-wrap items-center gap-2">
                    {c.rankable && c.rank != null ? (
                      <Badge variant="secondary">#{c.rank}</Badge>
                    ) : (
                      <Badge variant="outline">review</Badge>
                    )}
                    <Badge variant="outline">{c.kind}</Badge>
                    {c.category && <Badge variant="secondary">{c.category}</Badge>}
                    <span className="text-muted-foreground flex-1">{c.rationale}</span>
                  </div>
                  {c.rankable && (
                    <p className="text-muted-foreground">
                      cost Δ {formatDelta(c.cost_delta)}
                      {c.partitions_delta != null && <> · partitions Δ {formatDelta(c.partitions_delta)}</>}
                      {c.improved && c.improved.length > 0 && <> · {c.improved.join(", ")}</>}
                    </p>
                  )}
                  {c.sql && (
                    <div className="flex flex-wrap gap-2 items-start">
                      <pre className="flex-1 font-mono whitespace-pre-wrap break-all bg-muted/40 rounded p-2 max-h-28 overflow-auto">
                        {c.sql}
                      </pre>
                      <Button
                        size="sm"
                        variant="outline"
                        onClick={() => {
                          setCandidateSql(c.sql || "");
                          setSuggestRationale(c.rationale || "");
                        }}
                      >
                        Use
                      </Button>
                    </div>
                  )}
                  {c.ddl && (
                    <pre className="font-mono whitespace-pre-wrap break-all bg-muted/40 rounded p-2 max-h-28 overflow-auto">
                      {c.ddl}
                    </pre>
                  )}
                </div>
              ))}
            </div>
          )}
          {investigation.comparison && (
            <PlanCompare comparison={investigation.comparison} />
          )}
        </CardContent>
      </Card>

      {/* Generate report */}
      <Card className="panel-accent-top">
        <CardContent className="p-5 flex flex-col sm:flex-row sm:items-center sm:justify-between gap-4">
          <div>
            <p className="font-medium">Engineering investigation report</p>
            <p className="text-xs text-muted-foreground mt-1">
              Generate a shareable report with PostgreSQL evidence, plan findings, and recommended next actions.
            </p>
          </div>
          <Button onClick={() => void generateReport()} disabled={actionLoading === "report"}>
            {actionLoading === "report" ? <Loader2 className="h-4 w-4 animate-spin" /> : <FileText className="h-4 w-4" />}
            Generate report
          </Button>
        </CardContent>
      </Card>
    </div>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <p className="text-muted-foreground text-xs">{label}</p>
      <p className="font-semibold">{value}</p>
    </div>
  );
}

function formatDelta(n?: number) {
  if (n == null || Number.isNaN(n)) return "—";
  const rounded = Math.abs(n) >= 10 ? n.toFixed(0) : n.toFixed(2);
  return n > 0 ? `+${rounded}` : rounded;
}

function InvestigateLanding() {
  const navigate = useNavigate();
  const [scenarios, setScenarios] = useState<Awaited<ReturnType<typeof api.getDemoScenarios>>["items"]>([]);
  const [regressions, setRegressions] = useState<Awaited<ReturnType<typeof api.getRegressions>>["items"]>([]);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    Promise.allSettled([api.getDemoScenarios(), api.getRegressions(5)]).then(([s, r]) => {
      if (s.status === "fulfilled") setScenarios(s.value.items ?? []);
      if (r.status === "fulfilled") setRegressions(r.value.items ?? []);
      setLoading(false);
    });
  }, []);

  const startFromRegression = (title: string, query: string) => {
    const params = new URLSearchParams({ title, sql: query });
    navigate(`/investigate?${params.toString()}`);
  };

  const startDemo = (scenario: (typeof scenarios)[0]) => {
    const params = new URLSearchParams({ title: scenario.title, sql: scenario.sql });
    if (scenario.candidate_sql) params.set("candidate", scenario.candidate_sql);
    navigate(`/investigate?${params.toString()}`);
  };

  return (
    <div className="space-y-8">
      <div>
        <Badge variant="outline" className="mb-2">Query Investigation</Badge>
        <h1 className="text-2xl font-bold tracking-tight">Investigate a Query Regression</h1>
        <p className="text-muted-foreground mt-1 max-w-2xl">
          Select an expensive query from pg_stat_statements or start a guided demo.
          PgQueryNarrative automatically gathers plan evidence, compares improvements, and produces engineering-ready reports.
        </p>
      </div>

      <div className="grid gap-6 lg:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle className="text-base">Regression inbox</CardTitle>
            <CardDescription>Queries requiring attention</CardDescription>
          </CardHeader>
          <CardContent>
            {loading ? (
              <div className="space-y-2">{[1, 2, 3].map((i) => <Skeleton key={i} className="h-14 w-full" />)}</div>
            ) : regressions.length === 0 ? (
              <p className="text-sm text-muted-foreground py-4">No regressions detected. Check Query Stats for expensive queries.</p>
            ) : (
              <div className="space-y-2">
                {regressions.map((r) => (
                  <button
                    key={r.id}
                    type="button"
                    onClick={() => startFromRegression(r.title, r.query)}
                    className="w-full text-left p-3 rounded-lg border border-border hover:bg-muted/50 transition-colors"
                  >
                    <div className="flex items-center justify-between gap-2">
                      <span className="font-medium text-sm">{r.title}</span>
                      <Badge variant={r.impact === "critical" ? "destructive" : "outline"} className="text-[10px] capitalize">
                        {r.change_summary}
                      </Badge>
                    </div>
                    <p className="text-xs text-muted-foreground font-mono mt-1 truncate">{truncate(r.query, 60)}</p>
                  </button>
                ))}
              </div>
            )}
            <Link to="/stats" className="text-xs text-primary mt-3 inline-block">View all query stats →</Link>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle className="text-base">Guided demo scenarios</CardTitle>
            <CardDescription>Reproducible optimization investigations</CardDescription>
          </CardHeader>
          <CardContent>
            {loading ? (
              <div className="space-y-2">{[1, 2, 3].map((i) => <Skeleton key={i} className="h-14 w-full" />)}</div>
            ) : (
              <div className="space-y-2">
                {scenarios.map((s) => (
                  <button
                    key={s.id}
                    type="button"
                    onClick={() => startDemo(s)}
                    className="w-full text-left p-3 rounded-lg border border-border hover:bg-muted/50 transition-colors"
                  >
                    <div className="flex items-center justify-between gap-2">
                      <span className="font-medium text-sm">{s.title}</span>
                      <Badge variant="success" className="text-[10px]">{s.expected_improvement}</Badge>
                    </div>
                    <p className="text-xs text-muted-foreground mt-1">{s.problem}</p>
                  </button>
                ))}
              </div>
            )}
          </CardContent>
        </Card>
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">Start from SQL</CardTitle>
        </CardHeader>
        <CardContent>
          <Link to="/stats">
            <Button><Play className="h-4 w-4" /> Select from pg_stat_statements</Button>
          </Link>
        </CardContent>
      </Card>
    </div>
  );
}
