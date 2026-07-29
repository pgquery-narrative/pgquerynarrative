import { useEffect, useState } from "react";
import { Link } from "react-router";
import { Card, CardHeader, CardTitle, CardContent, CardDescription } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { api, type RegressionAlert, type WorkspaceOverview } from "@/api/client";
import {
  Search, Compass, FileText, Shield, GitBranch, Database, Clock,
  AlertTriangle, HardDrive, ScanLine, Play, ArrowRight,
} from "lucide-react";
import { cn, truncate, timeAgo } from "@/lib/utils";

const ENTRY_POINTS = [
  {
    to: "/investigate",
    icon: Search,
    title: "Investigate",
    description: "Find expensive or regressed queries",
    accent: "border-primary/30 hover:border-primary/50",
  },
  {
    to: "/query",
    icon: Compass,
    title: "Explore",
    description: "Run safe read-only PostgreSQL analysis",
    accent: "border-brand-blue/30 hover:border-brand-blue/50",
  },
  {
    to: "/query",
    icon: GitBranch,
    title: "Explain",
    description: "Understand an execution plan",
    accent: "border-brand-violet/30 hover:border-brand-violet/50",
  },
  {
    to: "/reports",
    icon: FileText,
    title: "Report",
    description: "Convert PostgreSQL evidence into a shareable report",
    accent: "border-success/30 hover:border-success/50",
  },
];

export default function Dashboard() {
  const [overview, setOverview] = useState<WorkspaceOverview | null>(null);
  const [regressions, setRegressions] = useState<RegressionAlert[]>([]);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    Promise.allSettled([api.getWorkspaceOverview(), api.getRegressions(5)]).then(([o, r]) => {
      if (o.status === "fulfilled") setOverview(o.value);
      if (r.status === "fulfilled") setRegressions(r.value.items ?? []);
      setLoading(false);
    });
  }, []);

  return (
    <div className="space-y-8">
      <div className="relative">
        <Badge variant="outline" className="mb-3">PostgreSQL Query Intelligence</Badge>
        <h1 className="text-3xl font-bold tracking-tight max-w-2xl">
          Investigate queries using the evidence that produced them
        </h1>
        <p className="text-muted-foreground mt-2 max-w-2xl">
          Workload statistics, SQL, query results, and execution plans — combined into
          evidence-backed explanations and engineering-ready reports.
        </p>
        <div className="flex flex-wrap gap-3 mt-5">
          <Link to="/investigate">
            <Button size="lg"><Play className="h-4 w-4" /> Start guided demo</Button>
          </Link>
          <Link to="/security">
            <Button variant="secondary" size="lg"><Shield className="h-4 w-4" /> Security & Trust</Button>
          </Link>
        </div>
      </div>

      {/* Entry points */}
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        {ENTRY_POINTS.map(({ to, icon: Icon, title, description, accent }) => (
          <Link key={title} to={to} className="group">
            <Card className={cn("h-full transition-all duration-200 hover:shadow-md panel-accent-top", accent)}>
              <CardContent className="p-5">
                <Icon className="h-6 w-6 text-primary mb-3 group-hover:scale-110 transition-transform" />
                <p className="font-semibold">{title}</p>
                <p className="text-xs text-muted-foreground mt-1">{description}</p>
                <ArrowRight className="h-4 w-4 text-muted-foreground mt-3 opacity-0 group-hover:opacity-100 transition-opacity" />
              </CardContent>
            </Card>
          </Link>
        ))}
      </div>

      {/* Evidence metrics */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">PostgreSQL evidence</CardTitle>
          <CardDescription>Workload intelligence from your connected database</CardDescription>
        </CardHeader>
        <CardContent>
          {loading ? (
            <div className="grid gap-4 sm:grid-cols-3 lg:grid-cols-6">
              {[1, 2, 3, 4, 5, 6].map((i) => <Skeleton key={i} className="h-16 w-full" />)}
            </div>
          ) : (
            <div className="grid gap-4 sm:grid-cols-3 lg:grid-cols-6">
              <EvidenceMetric icon={Database} label="Queries observed" value={overview?.queries_observed?.toLocaleString() ?? "—"} />
              <EvidenceMetric icon={Clock} label="Database time analyzed" value={overview ? `${overview.database_time_hours.toFixed(1)} hours` : "—"} />
              <EvidenceMetric icon={AlertTriangle} label="Queries requiring attention" value={String(overview?.queries_attention ?? "—")} highlight />
              <EvidenceMetric icon={GitBranch} label="Largest regression" value={overview ? `+${overview.largest_regression_pct.toFixed(0)}%` : "—"} highlight />
              <EvidenceMetric icon={HardDrive} label="Temporary data written" value={overview ? `${overview.temp_data_written_gb} GB` : "—"} />
              <EvidenceMetric icon={ScanLine} label="Sequential scans detected" value={String(overview?.sequential_scans_detected ?? "—")} />
            </div>
          )}
        </CardContent>
      </Card>

      {/* Regression inbox */}
      <Card>
        <CardHeader className="flex flex-row items-center justify-between">
          <div>
            <CardTitle className="text-base">Regression inbox</CardTitle>
            <CardDescription>Queries with significant performance changes</CardDescription>
          </div>
          <Link to="/investigate">
            <Button variant="secondary" size="sm">View all</Button>
          </Link>
        </CardHeader>
        <CardContent>
          {loading ? (
            <div className="space-y-2">{[1, 2, 3].map((i) => <Skeleton key={i} className="h-12 w-full" />)}</div>
          ) : regressions.length === 0 ? (
            <p className="text-sm text-muted-foreground py-4 text-center">No regressions detected. Your workload looks healthy.</p>
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b border-border/70 text-left text-xs text-muted-foreground">
                    <th className="pb-2 font-medium">Query</th>
                    <th className="pb-2 font-medium">Change</th>
                    <th className="pb-2 font-medium">Impact</th>
                    <th className="pb-2 font-medium">First detected</th>
                    <th className="pb-2" />
                  </tr>
                </thead>
                <tbody>
                  {regressions.map((r) => (
                    <tr key={r.id} className="border-b border-border/30 last:border-0">
                      <td className="py-3 font-medium">{r.title}</td>
                      <td className="py-3 text-muted-foreground">{r.change_summary}</td>
                      <td className="py-3">
                        <Badge variant={r.impact === "critical" ? "destructive" : r.impact === "high" ? "warning" : "secondary"} className="text-[10px] capitalize">
                          {r.impact}
                        </Badge>
                      </td>
                      <td className="py-3 text-muted-foreground text-xs">{timeAgo(r.first_detected_at)}</td>
                      <td className="py-3 text-right">
                        <Link
                          to={`/investigate?title=${encodeURIComponent(r.title)}&sql=${encodeURIComponent(r.query)}`}
                          className="text-xs text-primary hover:underline"
                        >
                          Investigate
                        </Link>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

function EvidenceMetric({
  icon: Icon,
  label,
  value,
  highlight,
}: {
  icon: React.ComponentType<{ className?: string }>;
  label: string;
  value: string;
  highlight?: boolean;
}) {
  return (
    <div className={cn("rounded-lg border p-3", highlight ? "border-warning/30 bg-warning/5" : "border-border/50")}>
      <Icon className="h-4 w-4 text-muted-foreground mb-1" />
      <p className="text-lg font-bold tracking-tight">{value}</p>
      <p className="text-[11px] text-muted-foreground mt-0.5">{label}</p>
    </div>
  );
}
