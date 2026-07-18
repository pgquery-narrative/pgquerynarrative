import { useEffect, useState } from "react";
import { Card, CardHeader, CardTitle, CardDescription, CardContent } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { api, type SavedQuery, type Schedule } from "@/api/client";

export default function SchedulesPage() {
  const [items, setItems] = useState<Schedule[]>([]);
  const [savedQueries, setSavedQueries] = useState<SavedQuery[]>([]);
  const [name, setName] = useState("");
  const [savedQueryID, setSavedQueryID] = useState("");
  const [intervalExpr, setIntervalExpr] = useState("@every 6h");
  const [target, setTarget] = useState("schedule-log");
  const [error, setError] = useState("");

  const load = async () => {
    const [s, q] = await Promise.all([api.listSchedules(), api.listSaved(200, 0)]);
    setItems(s.items || []);
    setSavedQueries(q.items || []);
  };

  useEffect(() => { void load(); }, []);

  const create = async () => {
    setError("");
    try {
      await api.createSchedule({
        name: name.trim(),
        saved_query_id: savedQueryID || undefined,
        interval_expr: intervalExpr.trim(),
        destination_type: "log",
        destination_target: target.trim() || "schedule-log",
      });
      setName("");
      await load();
    } catch (e) {
      setError(e instanceof Error ? e.message : "Failed to create schedule");
    }
  };

  const runNow = async (id: string) => {
    await api.runScheduleNow(id);
    await load();
  };

  const toggle = async (s: Schedule) => {
    await api.updateSchedule(s.id, { enabled: !s.enabled });
    await load();
  };

  const remove = async (id: string) => {
    await api.deleteSchedule(id);
    await load();
  };

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold tracking-tight">Schedules</h1>
        <p className="text-muted-foreground mt-1">Run saved queries on schedule and deliver generated reports.</p>
      </div>
      <Card>
        <CardHeader>
          <CardTitle className="text-sm">Create schedule</CardTitle>
          <CardDescription>Use `@every` duration format like `@every 1h` or `@every 24h`.</CardDescription>
        </CardHeader>
        <CardContent className="grid gap-2 md:grid-cols-4">
          <Input placeholder="Schedule name" value={name} onChange={(e) => setName(e.target.value)} />
          <select className="h-10 rounded-md border border-input bg-background px-3 text-sm" value={savedQueryID} onChange={(e) => setSavedQueryID(e.target.value)}>
            <option value="">Select saved query...</option>
            {savedQueries.map((q) => <option key={q.id} value={q.id}>{q.name}</option>)}
          </select>
          <Input placeholder="@every 6h" value={intervalExpr} onChange={(e) => setIntervalExpr(e.target.value)} />
          <Input placeholder="destination target" value={target} onChange={(e) => setTarget(e.target.value)} />
          <div className="md:col-span-4">
            <Button onClick={() => { void create(); }} disabled={!name.trim()}>Create Schedule</Button>
            {error && <p className="text-xs text-destructive mt-2">{error}</p>}
          </div>
        </CardContent>
      </Card>

      <div className="space-y-3">
        {items.map((s) => (
          <Card key={s.id}>
            <CardContent className="p-4 flex flex-wrap items-center justify-between gap-3">
              <div>
                <p className="font-medium">{s.name}</p>
                <p className="text-xs text-muted-foreground">
                  {s.interval_expr} • {s.destination_type}:{s.destination_target} • {s.enabled ? "enabled" : "disabled"}
                </p>
                <p className="text-xs text-muted-foreground">
                  last: {s.last_status || "never"} {s.last_run_at ? `at ${new Date(s.last_run_at).toLocaleString()}` : ""}
                </p>
              </div>
              <div className="flex gap-2">
                <Button size="sm" variant="outline" onClick={() => { void runNow(s.id); }}>Run now</Button>
                <Button size="sm" variant="outline" onClick={() => { void toggle(s); }}>{s.enabled ? "Disable" : "Enable"}</Button>
                <Button size="sm" variant="ghost" onClick={() => { void remove(s.id); }}>Delete</Button>
              </div>
            </CardContent>
          </Card>
        ))}
      </div>
    </div>
  );
}
