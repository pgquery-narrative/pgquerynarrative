import { useEffect, useMemo, useState } from "react";
import { Link, useParams } from "react-router";
import { api, type Dashboard, type DashboardWidgetInput, type DashboardResolved, type Report, type SavedQuery } from "@/api/client";
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { Trash2 } from "lucide-react";

function DashboardListPage() {
  const [items, setItems] = useState<Dashboard[]>([]);
  const [name, setName] = useState("");
  const [loading, setLoading] = useState(true);
  const load = () => api.listDashboards().then((r) => setItems(r.items || [])).finally(() => setLoading(false));
  useEffect(() => { void load(); }, []);

  const create = async () => {
    if (!name.trim()) return;
    await api.createDashboard(name.trim());
    setName("");
    await load();
  };
  const remove = async (id: string) => {
    await api.deleteDashboard(id);
    await load();
  };

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold tracking-tight">Dashboards</h1>
        <p className="text-muted-foreground mt-1">Pin report and saved-query widgets with refresh settings.</p>
      </div>
      <Card>
        <CardHeader><CardTitle className="text-sm">Create dashboard</CardTitle></CardHeader>
        <CardContent className="flex gap-2">
          <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="e.g. Daily Revenue Ops" />
          <Button onClick={() => { void create(); }}>Create</Button>
        </CardContent>
      </Card>
      {loading ? <Skeleton className="h-24 w-full" /> : (
        <div className="space-y-3">
          {items.map((d) => (
            <Card key={d.id}>
              <CardContent className="p-4 flex items-center justify-between gap-4">
                <div>
                  <p className="font-medium">{d.name}</p>
                  <p className="text-xs text-muted-foreground">{d.widgets.length} widgets</p>
                </div>
                <div className="flex gap-2">
                  <Link to={`/dashboards/${d.id}`}><Button variant="outline" size="sm">Open</Button></Link>
                  <Button variant="ghost" size="sm" onClick={() => { void remove(d.id); }}><Trash2 className="h-4 w-4" /></Button>
                </div>
              </CardContent>
            </Card>
          ))}
        </div>
      )}
    </div>
  );
}

function DashboardDetailPage() {
  const { id } = useParams<{ id: string }>();
  const [dashboard, setDashboard] = useState<Dashboard | null>(null);
  const [resolved, setResolved] = useState<DashboardResolved | null>(null);
  const [reports, setReports] = useState<Report[]>([]);
  const [savedQueries, setSavedQueries] = useState<SavedQuery[]>([]);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    if (!id) return;
    Promise.all([api.getDashboard(id), api.resolveDashboard(id), api.listReports(100, 0), api.listSaved(100, 0)])
      .then(([d, r, rs, sq]) => {
        setDashboard(d);
        setResolved(r);
        setReports(rs.items || []);
        setSavedQueries(sq.items || []);
      })
      .finally(() => setLoading(false));
  }, [id]);

  const refreshMs = useMemo(() => {
    const values = dashboard?.widgets.map((w) => Math.max(10, w.refresh_seconds || 300)) || [300];
    return Math.min(...values) * 1000;
  }, [dashboard]);

  useEffect(() => {
    if (!id || !refreshMs) return;
    const t = setInterval(() => {
      void api.resolveDashboard(id).then(setResolved).catch(() => {});
    }, refreshMs);
    return () => clearInterval(t);
  }, [id, refreshMs]);

  const save = async (nextWidgets: DashboardWidgetInput[]) => {
    if (!id || !dashboard) return;
    const updated = await api.updateDashboard(id, dashboard.name, nextWidgets);
    setDashboard(updated);
    setResolved(await api.resolveDashboard(id));
  };

  const addWidget = async (type: "report" | "saved_query", entityID: string) => {
    if (!dashboard || !entityID) return;
    const next = [...dashboard.widgets.map((w) => ({ ...w } as DashboardWidgetInput)), {
      widget_type: type,
      report_id: type === "report" ? entityID : undefined,
      saved_query_id: type === "saved_query" ? entityID : undefined,
      refresh_seconds: 300,
      position: dashboard.widgets.length,
    }];
    await save(next);
  };

  const removeWidget = async (index: number) => {
    if (!dashboard) return;
    const next = dashboard.widgets.filter((_, i) => i !== index).map((w, i) => ({ ...w, position: i }));
    await save(next);
  };

  if (loading) return <Skeleton className="h-48 w-full" />;
  if (!dashboard) return <p className="text-muted-foreground">Dashboard not found.</p>;

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold tracking-tight">{dashboard.name}</h1>
          <p className="text-xs text-muted-foreground">Auto refresh every {Math.round(refreshMs / 1000)}s</p>
        </div>
        <Link to="/dashboards"><Button variant="outline">Back</Button></Link>
      </div>
      <Card>
        <CardHeader><CardTitle className="text-sm">Add widgets</CardTitle></CardHeader>
        <CardContent className="grid gap-3 md:grid-cols-2">
          <select className="h-10 rounded-md border border-input bg-background px-3 text-sm" onChange={(e) => { void addWidget("report", e.target.value); e.currentTarget.value = ""; }}>
            <option value="">Add report widget...</option>
            {reports.map((r) => <option key={r.id} value={r.id}>{r.narrative?.headline || r.sql.slice(0, 50)}</option>)}
          </select>
          <select className="h-10 rounded-md border border-input bg-background px-3 text-sm" onChange={(e) => { void addWidget("saved_query", e.target.value); e.currentTarget.value = ""; }}>
            <option value="">Add saved query widget...</option>
            {savedQueries.map((q) => <option key={q.id} value={q.id}>{q.name}</option>)}
          </select>
        </CardContent>
      </Card>
      <div className="grid gap-4 md:grid-cols-2">
        {(resolved?.widgets || []).map((w, idx) => (
          <Card key={w.id}>
            <CardHeader className="pb-2">
              <CardTitle className="text-sm">{w.title || (w.widget_type === "report" ? "Report widget" : "Saved query widget")}</CardTitle>
              <CardDescription>{w.refresh_seconds}s refresh</CardDescription>
            </CardHeader>
            <CardContent className="space-y-2">
              {w.report && (
                <>
                  <p className="font-medium text-sm">{w.report.narrative?.headline || "Report"}</p>
                  <p className="text-xs text-muted-foreground">{w.report.sql}</p>
                </>
              )}
              {w.saved_query && (
                <>
                  <p className="font-medium text-sm">{w.saved_query.name}</p>
                  <p className="text-xs text-muted-foreground">{w.saved_query.sql}</p>
                </>
              )}
              <Button variant="ghost" size="sm" onClick={() => { void removeWidget(idx); }}>
                <Trash2 className="h-4 w-4" /> Remove
              </Button>
            </CardContent>
          </Card>
        ))}
      </div>
    </div>
  );
}

export default function DashboardsPage() {
  const { id } = useParams();
  return id ? <DashboardDetailPage /> : <DashboardListPage />;
}
