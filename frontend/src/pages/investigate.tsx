import { useCallback, useEffect, useRef, useState } from "react";
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
import { InvestigationVerdict } from "@/components/investigation-verdict";
import { InvestigationFix } from "@/components/investigation-fix";
import { api, type Investigation, type RankedCandidate, type RewriteCandidate, ApiError } from "@/api/client";
import {
  Search, FileText, GitCompare, CheckCircle2, ArrowRight, Play, Loader2, Sparkles, ListOrdered, ChevronRight,
} from "lucide-react";
import { cn, formatFloat, timeAgo, truncate } from "@/lib/utils";
import { equivalenceStatusOf, equivalenceLabel, equivalenceTone, isShippableEquivalence, normalizeEquivalenceStatus } from "@/lib/equivalence";

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
  const [bindsText, setBindsText] = useState("");
  const [verifyResults, setVerifyResults] = useState(true);
  const [suggestRationale, setSuggestRationale] = useState("");
  const [rankedCandidates, setRankedCandidates] = useState<RankedCandidate[]>([]);
  const [rankRecommendation, setRankRecommendation] = useState("");
  const [suggestedCandidates, setSuggestedCandidates] = useState<RewriteCandidate[]>([]);
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
      const queryid = searchParams.get("queryid");
      api.createInvestigation({
        title,
        sql,
        ...(queryid ? { queryid } : {}),
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
      const inv = await api.addInvestigationCandidate(
        investigation.id,
        candidateSql.trim(),
        true,
        parseBinds(bindsText),
        verifyResults,
      );
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
      const items = res.candidates ?? [];
      const top = items[0];
      if (!top?.sql) {
        setSuggestedCandidates([]);
        setError("No rewrite suggestions for this query. Supported: DATE_TRUNC / EXTRACT / to_char / ::date unwraps, COALESCE, column::text casts, OR → UNION ALL, and IN/NOT IN → EXISTS.");
        setSuggestRationale("");
        return;
      }
      setSuggestedCandidates(items);
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
      setRankRecommendation(res.recommendation ?? "");
      // Only prefill from a candidate that actually ranked #1 (improved on the
      // baseline) — never from a "not recommended" rewrite.
      const best = items.find((c) => c.rank === 1 && c.sql);
      if (best?.sql) {
        setCandidateSql(best.sql);
        setSuggestRationale(best.rationale || "");
      } else if (res.recommendation) {
        setError(res.recommendation);
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
    if (investigation.comparison) {
      const eq = equivalenceStatusOf(investigation.comparison);
      if (!isShippableEquivalence(eq)) {
        setError(
          eq === "Different"
            ? "Cannot generate a shippable report while result equivalence is Different. Fix the candidate rewrite first."
            : eq === "NotRequested"
              ? "Result equivalence was not checked. Re-run Compare plans with result verification enabled."
              : "Cannot generate a shippable report until result equivalence is VerifiedEqual (or SampleMatch for a large result). Re-run Compare plans with result verification."
        );
        return;
      }
    }
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

  const updateFix = async (body: { fix_status?: string; fix_reference?: string }) => {
    if (!investigation) return;
    setActionLoading("fix");
    setError("");
    try {
      setInvestigation(await api.updateInvestigationFix(investigation.id, body));
    } catch (e) {
      setError(e instanceof ApiError ? e.message : "Failed to update fix status");
    } finally {
      setActionLoading("");
    }
  };

  const currentStep = investigation?.comparison
    ? investigation.report_id ? 5 : 3
    : investigation?.explain ? 2 : 0;

  const candidateRef = useRef<HTMLDivElement>(null);
  const topRanked = rankedCandidates.find((c) => c.rank === 1 && c.sql);
  const baselineCost = investigation?.explain?.total_cost;
  const projected = topRanked
    ? {
        costPct:
          topRanked.total_cost != null && baselineCost
            ? Math.round(((topRanked.total_cost - baselineCost) / baselineCost) * 100)
            : undefined,
        partitionsBefore:
          topRanked.partitions_scanned != null && topRanked.partitions_delta != null
            ? topRanked.partitions_scanned - topRanked.partitions_delta
            : undefined,
        partitionsAfter: topRanked.partitions_scanned ?? undefined,
      }
    : null;
  const jumpToFix = () => {
    if (!rankedCandidates.length && !suggestedCandidates.length) void suggestRewrite();
    setTimeout(() => candidateRef.current?.scrollIntoView({ behavior: "smooth", block: "start" }), 80);
  };

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
          {investigation.query_fingerprint && (
            <p className="text-muted-foreground mt-1 text-xs font-mono">{investigation.query_fingerprint}</p>
          )}
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

      {/* Verdict — root cause + recommended fix, ahead of the raw plan */}
      {investigation.explain?.diagnosis && (
        <InvestigationVerdict
          diagnosis={investigation.explain.diagnosis}
          projected={projected}
          onFix={jumpToFix}
          fixLabel={rankedCandidates.length || suggestedCandidates.length ? "Jump to rewrite" : "Preview the fix"}
          fixBusy={actionLoading === "suggest"}
        />
      )}

      {investigation.explain?.generic_plan && (
        <p className="text-xs text-muted-foreground -mt-2">
          Parameterized query ($1, $2, …): plan and costs are from <code>EXPLAIN (GENERIC_PLAN)</code>. Row counts and
          partition pruning use planner defaults for the unbound parameters — supply sample bind values below for an
          executed compare.
        </p>
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
        </CardHeader>
        <CardContent>
          <pre className="text-xs font-mono bg-muted/40 rounded-lg p-4 overflow-x-auto whitespace-pre-wrap">{investigation.sql}</pre>
        </CardContent>
      </Card>

      {/* Raw plan analysis — collapsed by default; the verdict above is the summary */}
      {investigation.explain && (
        <Card>
          <details className="group">
            <summary className="flex items-center justify-between gap-2 p-4 cursor-pointer list-none select-none">
              <span className="text-sm font-medium flex items-center gap-2">
                <ChevronRight className="h-4 w-4 transition-transform group-open:rotate-90" />
                Raw plan analysis
              </span>
              <span className="text-xs text-muted-foreground">
                cost {formatFloat(investigation.explain.total_cost)} · {investigation.explain.findings?.length ?? 0} findings
              </span>
            </summary>
            <CardContent className="pt-0">
              <PlanExplorer plan={investigation.explain.plan} findings={investigation.explain.findings} />
            </CardContent>
          </details>
        </Card>
      )}

      {/* Candidate comparison */}
      <Card ref={candidateRef}>
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
            placeholder="Paste candidate SQL rewrite, or click Suggest rewrite / Rank candidates…"
            className="font-mono text-xs min-h-[100px]"
          />
          {suggestRationale && (
            <p className="text-xs text-muted-foreground">{suggestRationale}</p>
          )}
          {investigation.explain?.generic_plan && (
            <div className="space-y-1">
              <label className="text-xs font-medium text-muted-foreground">
                Bind values — one per line, e.g. <code>$1 = 2025-01-01</code>. Needed to run an executed compare and
                equivalence check on a parameterized query.
              </label>
              <Textarea
                value={bindsText}
                onChange={(e) => setBindsText(e.target.value)}
                placeholder={"$1 = 2025-01-01"}
                className="font-mono text-xs min-h-[60px]"
              />
            </div>
          )}
          <label className="flex items-center gap-2 text-xs text-muted-foreground">
            <input
              type="checkbox"
              checked={verifyResults}
              onChange={(e) => setVerifyResults(e.target.checked)}
              className="h-3.5 w-3.5 accent-primary"
            />
            Verify result equivalence — runs both queries (COUNT(*) + a bounded sample). Requires the query permission.
          </label>
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
          {suggestedCandidates.length > 0 && (
            <div className="space-y-2">
              <p className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">Suggested rewrites</p>
              {suggestedCandidates.map((c, i) => (
                <div key={`suggest-${i}`} className="rounded-md border border-border/50 p-3 space-y-2 text-xs">
                  <div className="flex flex-wrap items-center gap-2">
                    {c.category && <Badge variant="secondary">{c.category}</Badge>}
                    {c.confidence && <Badge variant="outline">{c.confidence}</Badge>}
                    <span className="text-muted-foreground flex-1">{c.rationale}</span>
                  </div>
                  <div className="flex flex-wrap gap-2 items-start">
                    <pre className="flex-1 font-mono whitespace-pre-wrap break-all bg-muted/40 rounded p-2 max-h-28 overflow-auto">
                      {c.sql}
                    </pre>
                    <Button
                      size="sm"
                      variant="outline"
                      onClick={() => {
                        setCandidateSql(c.sql);
                        setSuggestRationale(c.rationale || "");
                      }}
                    >
                      Use
                    </Button>
                  </div>
                </div>
              ))}
            </div>
          )}
          {rankRecommendation && (
            <p className="rounded-md border border-amber-500/40 bg-amber-500/10 p-3 text-xs text-amber-900 dark:text-amber-100">
              {rankRecommendation}
            </p>
          )}
          {rankedCandidates.length > 0 && (
            <div className="space-y-2">
              <p className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">Ranked candidates</p>
              {rankedCandidates.map((c, i) => (
                <div key={i} className="rounded-md border border-border/50 p-3 space-y-2 text-xs">
                  <div className="flex flex-wrap items-center gap-2">
                    {c.rankable && c.rank != null ? (
                      <Badge variant="secondary">#{c.rank}</Badge>
                    ) : c.rankable ? (
                      <Badge variant="outline" className="border-amber-500/50 text-amber-800 dark:text-amber-200">not recommended</Badge>
                    ) : (
                      <Badge variant="outline">review</Badge>
                    )}
                    <Badge variant="outline">{c.kind}</Badge>
                    {c.projection_method && c.projection_method !== "unavailable" && (
                      <Badge
                        variant={c.projection_method === "hypopg" ? "success" : "outline"}
                        className={c.projection_method === "heuristic" ? "border-amber-500/50 text-amber-800 dark:text-amber-200" : undefined}
                      >
                        {c.projection_method === "heuristic" ? "heuristic (not planner)" : c.projection_method}
                      </Badge>
                    )}
                    {c.kind === "index_ddl" && c.projection_method === "heuristic" && (
                      <span className="text-[10px] text-amber-800 dark:text-amber-200">Review-only cost — not ranked with hypopg</span>
                    )}
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

          {investigation.candidates && investigation.candidates.length > 1 && (
            <div className="space-y-2">
              <p className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
                Tested candidates ({investigation.candidates.length})
              </p>
              {investigation.candidates.map((c) => (
                <div
                  key={c.id}
                  className={cn(
                    "rounded-md border p-2.5 space-y-1.5 text-xs",
                    c.is_current ? "border-primary/40 bg-primary/5" : "border-border/50",
                  )}
                >
                  <div className="flex flex-wrap items-center gap-2">
                    {c.is_current && <Badge variant="secondary" className="text-[10px]">current</Badge>}
                    {c.source && c.source !== "manual" && (
                      <Badge variant="outline" className="text-[10px]">{c.source}</Badge>
                    )}
                    {c.equivalence_status && (() => {
                      const st = normalizeEquivalenceStatus(c.equivalence_status);
                      const tone = equivalenceTone(st);
                      return (
                        <Badge
                          variant={
                            tone === "success"
                              ? "success"
                              : tone === "warning"
                                ? "warning"
                                : tone === "destructive"
                                  ? "destructive"
                                  : "outline"
                          }
                          className="text-[10px]"
                        >
                          {equivalenceLabel(st)}
                        </Badge>
                      );
                    })()}
                    {typeof c.cost_delta === "number" && (
                      <span className="text-muted-foreground">
                        cost Δ {formatDelta(c.cost_delta)}
                      </span>
                    )}
                    <span className="text-muted-foreground/60 ml-auto">{timeAgo(c.created_at)}</span>
                  </div>
                  <div className="flex flex-wrap gap-2 items-start">
                    <pre className="flex-1 font-mono whitespace-pre-wrap break-all bg-muted/40 rounded p-2 max-h-24 overflow-auto">
                      {c.candidate_sql}
                    </pre>
                    {!c.is_current && (
                      <Button
                        size="sm"
                        variant="outline"
                        onClick={() => {
                          setCandidateSql(c.candidate_sql);
                          setSuggestRationale("");
                          if (c.binds?.length) setBindsText(c.binds.map((b, i) => `$${i + 1} = ${b}`).join("\n"));
                        }}
                      >
                        Re-test
                      </Button>
                    )}
                  </div>
                </div>
              ))}
            </div>
          )}
        </CardContent>
      </Card>

      {/* Generate report */}
      <Card className="panel-accent-top">
        <CardContent className="p-5 flex flex-col sm:flex-row sm:items-center sm:justify-between gap-4">
          <div>
            <p className="font-medium">Engineering investigation report</p>
            <p className="text-xs text-muted-foreground mt-1">
              {investigation.comparison
                ? "Result equivalence must be VerifiedEqual (or SampleMatch for a large result) to ship a report with a rewrite."
                : "Generates a findings-only report from the diagnosis above."}
            </p>
          </div>
          <Button
            onClick={() => void generateReport()}
            disabled={
              actionLoading === "report" ||
              (!!investigation.comparison &&
                !isShippableEquivalence(equivalenceStatusOf(investigation.comparison)))
            }
          >
            {actionLoading === "report" ? <Loader2 className="h-4 w-4 animate-spin" /> : <FileText className="h-4 w-4" />}
            Generate report
          </Button>
        </CardContent>
      </Card>

      {investigation.report_id && (
        <InvestigationFix
          investigation={investigation}
          busy={actionLoading === "fix"}
          onUpdate={(body) => void updateFix(body)}
        />
      )}
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

/** Parse "$1 = 2025-01-01" lines into an ordered bind array. Gaps stay "". */
function parseBinds(text: string): string[] {
  const out: string[] = [];
  for (const line of text.split("\n")) {
    const m = line.match(/^\s*\$?(\d+)\s*=\s*(.+?)\s*$/);
    if (!m) continue;
    const idx = Number(m[1]) - 1;
    if (idx < 0 || idx > 63) continue;
    while (out.length <= idx) out.push("");
    out[idx] = m[2];
  }
  return out;
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

  const startFromRegression = async (alert: (typeof regressions)[0]) => {
    if (alert.investigation_id) {
      navigate(`/investigate/${alert.investigation_id}`);
      return;
    }
    setLoading(true);
    try {
      const inv = await api.createInvestigationFromRegression(alert.id);
      navigate(`/investigate/${inv.id}`);
    } catch {
      // Fallback: paste SQL into create flow when alert SQL is incomplete.
      const params = new URLSearchParams({ title: alert.title, sql: alert.query });
      navigate(`/investigate?${params.toString()}`);
    } finally {
      setLoading(false);
    }
  };

  const startDemo = (scenario: (typeof scenarios)[0]) => {
    // Problem SQL only — do not prefill answer-key candidate_sql.
    // Rewrites come from Suggest rewrite / Rank candidates.
    const params = new URLSearchParams({ title: scenario.title, sql: scenario.sql });
    navigate(`/investigate?${params.toString()}`);
  };

  return (
    <div className="space-y-8">
      <div>
        <Badge variant="outline" className="mb-2">Query Investigation</Badge>
        <h1 className="text-2xl font-bold tracking-tight">Investigate a Query Regression</h1>
        <p className="text-muted-foreground mt-1 max-w-2xl">
          Select an expensive query from pg_stat_statements or start a guided demo.
          PgQueryNarrative gathers EXPLAIN evidence, proposes candidate rewrites from plan findings,
          and compares before/after plans — then packages an engineering report. You still review
          and validate equivalence before changing production SQL.
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
                    onClick={() => void startFromRegression(r)}
                    className="w-full text-left p-3 rounded-lg border border-border hover:bg-muted/50 transition-colors"
                  >
                    <div className="flex items-center justify-between gap-2">
                      <span className="font-medium text-sm">{r.title}</span>
                      <Badge variant={r.impact === "critical" ? "destructive" : "outline"} className="text-[10px] capitalize">
                        {r.investigation_id ? "open" : r.change_summary}
                      </Badge>
                    </div>
                    <p className="text-xs text-muted-foreground font-mono mt-1 truncate">{truncate(r.query, 60)}</p>
                    {typeof r.occurrences === "number" && r.occurrences > 1 && (
                      <p className="text-[11px] text-muted-foreground mt-0.5">
                        seen in {r.occurrences} consecutive polls
                        {r.previous_alert_id ? " · recurred after recovering" : ""}
                      </p>
                    )}
                  </button>
                ))}
              </div>
            )}
            <Link to="/stats" className="text-xs text-primary mt-3 inline-block">View all query stats →</Link>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle className="text-base">Sample problem queries</CardTitle>
            <CardDescription>
              Start from known-bad SQL, then use Suggest rewrite / Rank candidates — no answer key is prefilled.
            </CardDescription>
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
