import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { Card, CardHeader, CardTitle, CardContent, CardDescription } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { api, type Report, type SavedQuery } from "@/api/client";
import { Terminal, FileText, Bookmark, Database, Zap, Clock } from "lucide-react";
import { buttonVariants } from "@/components/ui/button";
import { cn, truncate } from "@/lib/utils";

export default function Dashboard() {
  const [reports, setReports] = useState<Report[]>([]);
  const [saved, setSaved] = useState<SavedQuery[]>([]);
  const [schemaCount, setSchemaCount] = useState<number | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    Promise.allSettled([
      api.listReports(5, 0),
      api.listSaved(5, 0),
      api.getSchema(),
    ]).then(([r, s, sc]) => {
      if (r.status === "fulfilled") setReports(r.value.items || []);
      if (s.status === "fulfilled") setSaved(s.value.items || []);
      if (sc.status === "fulfilled") {
        const tables = sc.value.schemas.reduce((n, s) => n + s.tables.length, 0);
        setSchemaCount(tables);
      }
      setLoading(false);
    });
  }, []);

  return (
    <div className="space-y-8">
      <div>
        <h1 className="text-2xl font-bold tracking-tight">Dashboard</h1>
        <p className="text-muted-foreground mt-1">Overview of system health, activity, and quick actions.</p>
      </div>

      <Card className="panel-accent-top">
        <CardContent className="p-5 flex flex-col md:flex-row md:items-center md:justify-between gap-4">
          <div>
            <p className="text-sm font-medium">System status</p>
            <p className="text-xs text-muted-foreground mt-1">App and API are running. Use Query Runner for execution and Reports for narratives.</p>
          </div>
          <Badge variant="success">Operational</Badge>
        </CardContent>
      </Card>

      {/* Status cards */}
      <div className="grid gap-4 md:grid-cols-3">
        <Card>
          <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
            <CardTitle className="text-sm font-medium">Database</CardTitle>
            <Database className="h-4 w-4 text-muted-foreground" />
          </CardHeader>
          <CardContent>
            {loading ? <Skeleton className="h-6 w-24" /> : (
              <>
                <div className="text-2xl font-bold">{schemaCount ?? "—"} tables</div>
                <Badge variant="success" className="mt-1">Connected</Badge>
              </>
            )}
          </CardContent>
        </Card>
        <Card>
          <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
            <CardTitle className="text-sm font-medium">Reports</CardTitle>
            <FileText className="h-4 w-4 text-muted-foreground" />
          </CardHeader>
          <CardContent>
            {loading ? <Skeleton className="h-6 w-16" /> : <div className="text-2xl font-bold">{reports.length}</div>}
            <p className="text-xs text-muted-foreground mt-1">recent</p>
          </CardContent>
        </Card>
        <Card>
          <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
            <CardTitle className="text-sm font-medium">Saved Queries</CardTitle>
            <Bookmark className="h-4 w-4 text-muted-foreground" />
          </CardHeader>
          <CardContent>
            {loading ? <Skeleton className="h-6 w-16" /> : <div className="text-2xl font-bold">{saved.length}</div>}
            <p className="text-xs text-muted-foreground mt-1">total</p>
          </CardContent>
        </Card>
      </div>

      {/* Quick actions */}
      <div className="flex flex-wrap gap-3">
        <Link to="/query"><Button><Terminal className="h-4 w-4" /> Run Query</Button></Link>
        <Link to="/reports"><Button variant="secondary"><FileText className="h-4 w-4" /> View Reports</Button></Link>
        <Link to="/saved"><Button variant="secondary"><Bookmark className="h-4 w-4" /> Saved Queries</Button></Link>
      </div>

      {/* Recent activity */}
      <div className="grid gap-6 md:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle>Recent Reports</CardTitle>
            <CardDescription>Last generated reports</CardDescription>
          </CardHeader>
          <CardContent>
            {loading ? (
              <div className="space-y-3">{[1,2,3].map(i => <Skeleton key={i} className="h-12 w-full" />)}</div>
            ) : reports.length === 0 ? (
              <div className="flex flex-col items-center gap-3 py-4 text-center">
                <p className="text-sm text-muted-foreground">No reports yet.</p>
                <Link to="/query" className={cn(buttonVariants({ size: "sm" }), "!text-white hover:!text-white dark:!text-black dark:hover:!text-black")}>Run a query</Link>
              </div>
            ) : (
              <div className="space-y-3">
                {reports.map(r => (
                  <Link key={r.id} to={`/reports/${r.id}`} className="flex items-center justify-between p-3 rounded-md border border-border hover:bg-secondary/50 transition-colors">
                    <div className="min-w-0">
                      <p className="text-sm font-medium truncate">{r.narrative?.headline || truncate(r.sql, 50)}</p>
                      <div className="flex items-center gap-2 mt-1">
                        <Clock className="h-3 w-3 text-muted-foreground" />
                        <span className="text-xs text-muted-foreground">{new Date(r.created_at).toLocaleDateString()}</span>
                        <Badge variant="secondary" className="text-[10px]">{r.llm_provider}</Badge>
                        <Badge variant="outline" className="text-[10px]">{r.connection_id}</Badge>
                      </div>
                    </div>
                    <Zap className="h-4 w-4 text-brand-cyan flex-shrink-0" />
                  </Link>
                ))}
              </div>
            )}
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>Saved Queries</CardTitle>
            <CardDescription>Your bookmarked queries</CardDescription>
          </CardHeader>
          <CardContent>
            {loading ? (
              <div className="space-y-3">{[1,2,3].map(i => <Skeleton key={i} className="h-12 w-full" />)}</div>
            ) : saved.length === 0 ? (
              <div className="flex flex-col items-center gap-3 py-4 text-center">
                <p className="text-sm text-muted-foreground">No saved queries yet.</p>
                <Link to="/query" className={cn(buttonVariants({ size: "sm" }), "!text-white hover:!text-white dark:!text-black dark:hover:!text-black")}>Go to Query Runner</Link>
              </div>
            ) : (
              <div className="space-y-3">
                {saved.map(q => (
                  <div key={q.id} className="flex items-center justify-between p-3 rounded-md border border-border">
                    <div className="min-w-0">
                      <p className="text-sm font-medium truncate">{q.name}</p>
                      <p className="text-xs text-muted-foreground font-mono truncate mt-0.5">{truncate(q.sql, 60)}</p>
                    </div>
                    <div className="flex items-center gap-2">
                      <Badge variant="secondary" className="text-[10px]">{q.connection_id}</Badge>
                      {q.tags && q.tags.length > 0 && <Badge variant="outline" className="ml-2 text-[10px]">{q.tags[0]}</Badge>}
                    </div>
                  </div>
                ))}
              </div>
            )}
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
