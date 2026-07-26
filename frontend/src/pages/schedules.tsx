import { useEffect, useState } from "react";
import { Card, CardHeader, CardTitle, CardDescription, CardContent } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  api,
  type SavedQuery,
  type Schedule,
  type ScheduleRun,
  type WebhookDelivery,
} from "@/api/client";

type DestinationType = "log" | "webhook";

export default function SchedulesPage() {
  const [items, setItems] = useState<Schedule[]>([]);
  const [savedQueries, setSavedQueries] = useState<SavedQuery[]>([]);
  const [deliveries, setDeliveries] = useState<WebhookDelivery[]>([]);
  const [name, setName] = useState("");
  const [savedQueryID, setSavedQueryID] = useState("");
  const [intervalExpr, setIntervalExpr] = useState("@every 6h");
  const [destinationType, setDestinationType] = useState<DestinationType>("log");
  const [target, setTarget] = useState("schedule-log");
  const [error, setError] = useState("");
  const [busyId, setBusyId] = useState<string | null>(null);
  const [expandedId, setExpandedId] = useState<string | null>(null);
  const [runsBySchedule, setRunsBySchedule] = useState<Record<string, ScheduleRun[]>>({});

  const load = async () => {
    // Load independently so a schedules API failure does not blank the saved-query dropdown.
    const [schedResult, savedResult, deliveryResult] = await Promise.allSettled([
      api.listSchedules(),
      api.listSaved(100, 0),
      api.listWebhookDeliveries(),
    ]);
    if (schedResult.status === "fulfilled") {
      setItems(schedResult.value.items || []);
    } else {
      setError(schedResult.reason instanceof Error ? schedResult.reason.message : "Failed to load schedules");
    }
    if (savedResult.status === "fulfilled") {
      setSavedQueries(savedResult.value.items || []);
    } else {
      setError(savedResult.reason instanceof Error ? savedResult.reason.message : "Failed to load saved queries");
    }
    if (deliveryResult.status === "fulfilled") {
      setDeliveries(deliveryResult.value.items || []);
    }
  };

  useEffect(() => { void load(); }, []);

  const create = async () => {
    setError("");
    if (!savedQueryID) {
      setError("Select a saved query");
      return;
    }
    if (destinationType === "webhook" && !target.trim()) {
      setError("Webhook destination URL is required");
      return;
    }
    try {
      await api.createSchedule({
        name: name.trim(),
        saved_query_id: savedQueryID,
        interval_expr: intervalExpr.trim(),
        destination_type: destinationType,
        destination_target: target.trim() || (destinationType === "log" ? "schedule-log" : ""),
      });
      setName("");
      await load();
    } catch (e) {
      setError(e instanceof Error ? e.message : "Failed to create schedule");
    }
  };

  const runNow = async (id: string) => {
    setError("");
    setBusyId(id);
    try {
      await api.runScheduleNow(id);
      await load();
      if (expandedId === id) {
        await loadRuns(id);
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : "Failed to run schedule");
    } finally {
      setBusyId(null);
    }
  };

  const toggle = async (s: Schedule) => {
    await api.updateSchedule(s.id, { enabled: !s.enabled });
    await load();
  };

  const remove = async (id: string) => {
    await api.deleteSchedule(id);
    if (expandedId === id) {
      setExpandedId(null);
    }
    await load();
  };

  const loadRuns = async (id: string) => {
    try {
      const result = await api.listScheduleRuns(id);
      setRunsBySchedule((prev) => ({ ...prev, [id]: result.items || [] }));
    } catch (e) {
      setError(e instanceof Error ? e.message : "Failed to load schedule runs");
    }
  };

  const toggleRuns = async (id: string) => {
    if (expandedId === id) {
      setExpandedId(null);
      return;
    }
    setExpandedId(id);
    await loadRuns(id);
  };

  const retryRun = async (scheduleId: string, runId: string) => {
    setError("");
    setBusyId(runId);
    try {
      await api.retryScheduleRun(runId);
      await load();
      await loadRuns(scheduleId);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Failed to retry run");
    } finally {
      setBusyId(null);
    }
  };

  const onDestinationTypeChange = (next: DestinationType) => {
    setDestinationType(next);
    if (next === "log" && (target.startsWith("http://") || target.startsWith("https://") || !target.trim())) {
      setTarget("schedule-log");
    }
    if (next === "webhook" && (!target.trim() || target === "schedule-log")) {
      setTarget("");
    }
  };

  return (
    <div data-testid="schedules-page" className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold tracking-tight">Schedules</h1>
        <p className="text-muted-foreground mt-1">Run saved queries on schedule and deliver generated reports.</p>
      </div>
      <Card data-testid="schedule-create-panel">
        <CardHeader>
          <CardTitle className="text-sm">Create schedule</CardTitle>
          <CardDescription>
            Use `@every` duration format like `@every 1h`. Webhook destinations must be on the server allowlist.
          </CardDescription>
        </CardHeader>
        <CardContent className="grid gap-2 md:grid-cols-5">
          <Input
            data-testid="schedule-name"
            placeholder="Schedule name"
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
          <select
            data-testid="schedule-saved-query"
            className="h-10 rounded-md border border-input bg-background px-3 text-sm"
            value={savedQueryID}
            onChange={(e) => setSavedQueryID(e.target.value)}
          >
            <option value="">Select saved query...</option>
            {savedQueries.map((q) => <option key={q.id} value={q.id}>{q.name}</option>)}
          </select>
          <Input
            data-testid="schedule-interval"
            placeholder="@every 6h"
            value={intervalExpr}
            onChange={(e) => setIntervalExpr(e.target.value)}
          />
          <select
            data-testid="schedule-destination-type"
            className="h-10 rounded-md border border-input bg-background px-3 text-sm"
            value={destinationType}
            onChange={(e) => onDestinationTypeChange(e.target.value as DestinationType)}
          >
            <option value="log">Log</option>
            <option value="webhook">Webhook</option>
          </select>
          <Input
            data-testid="schedule-target"
            placeholder={destinationType === "webhook" ? "https://hooks.example.com/…" : "log label"}
            value={target}
            onChange={(e) => setTarget(e.target.value)}
          />
          <div className="md:col-span-5">
            <Button data-testid="schedule-create" onClick={() => { void create(); }} disabled={!name.trim()}>
              Create Schedule
            </Button>
            {error && <p data-testid="schedule-error" className="text-xs text-destructive mt-2">{error}</p>}
          </div>
        </CardContent>
      </Card>

      <div data-testid="schedules-list" className="space-y-3">
        {items.map((s) => (
          <Card key={s.id} data-testid="schedule-item" data-schedule-name={s.name} data-schedule-id={s.id}>
            <CardContent className="p-4 space-y-3">
              <div className="flex flex-wrap items-center justify-between gap-3">
                <div>
                  <p className="font-medium">{s.name}</p>
                  <p className="text-xs text-muted-foreground">
                    {s.interval_expr} • {s.destination_type}:{s.destination_target} • {s.enabled ? "enabled" : "disabled"}
                  </p>
                  <p data-testid="schedule-last-status" className="text-xs text-muted-foreground">
                    last: {s.last_status || "never"} {s.last_run_at ? `at ${new Date(s.last_run_at).toLocaleString()}` : ""}
                  </p>
                  {s.last_error && (
                    <p className="text-xs text-destructive mt-1">{s.last_error}</p>
                  )}
                </div>
                <div className="flex flex-wrap gap-2">
                  <Button
                    data-testid="schedule-run-now"
                    size="sm"
                    variant="outline"
                    disabled={busyId === s.id}
                    onClick={() => { void runNow(s.id); }}
                  >
                    Run now
                  </Button>
                  <Button
                    data-testid="schedule-toggle-runs"
                    size="sm"
                    variant="outline"
                    onClick={() => { void toggleRuns(s.id); }}
                  >
                    {expandedId === s.id ? "Hide runs" : "Runs"}
                  </Button>
                  <Button data-testid="schedule-toggle" size="sm" variant="outline" onClick={() => { void toggle(s); }}>
                    {s.enabled ? "Disable" : "Enable"}
                  </Button>
                  <Button data-testid="schedule-delete" size="sm" variant="ghost" onClick={() => { void remove(s.id); }}>
                    Delete
                  </Button>
                </div>
              </div>

              {expandedId === s.id && (
                <div data-testid="schedule-runs" className="rounded-md border border-border p-3 space-y-2">
                  <p className="text-xs font-medium text-muted-foreground">Recent runs</p>
                  {(runsBySchedule[s.id] || []).length === 0 && (
                    <p className="text-xs text-muted-foreground">No runs yet.</p>
                  )}
                  {(runsBySchedule[s.id] || []).map((run) => (
                    <div
                      key={run.id}
                      data-testid="schedule-run-item"
                      className="flex flex-wrap items-center justify-between gap-2 text-xs"
                    >
                      <div>
                        <span className="font-medium">{run.status}</span>
                        {" · "}
                        attempt {run.attempt_count}
                        {" · "}
                        {new Date(run.scheduled_for).toLocaleString()}
                        {run.failure_message ? ` · ${run.failure_message}` : ""}
                      </div>
                      {(run.status === "failed" || run.status === "dead_letter") && (
                        <Button
                          data-testid="schedule-run-retry"
                          size="sm"
                          variant="outline"
                          disabled={busyId === run.id}
                          onClick={() => { void retryRun(s.id, run.id); }}
                        >
                          Retry
                        </Button>
                      )}
                    </div>
                  ))}
                </div>
              )}
            </CardContent>
          </Card>
        ))}
      </div>

      <Card data-testid="webhook-deliveries-panel">
        <CardHeader>
          <CardTitle className="text-sm">Webhook deliveries</CardTitle>
          <CardDescription>Recent delivery attempts across schedules in this organization.</CardDescription>
        </CardHeader>
        <CardContent className="space-y-2">
          {deliveries.length === 0 && (
            <p className="text-xs text-muted-foreground">No webhook deliveries yet.</p>
          )}
          {deliveries.map((d) => (
            <div
              key={d.id}
              data-testid="webhook-delivery-item"
              className="flex flex-wrap items-center justify-between gap-2 text-xs border-b border-border pb-2 last:border-0"
            >
              <div>
                <span className="font-medium">{d.status}</span>
                {d.http_status != null ? ` · HTTP ${d.http_status}` : ""}
                {" · "}
                {d.destination_url}
                {d.error_message ? ` · ${d.error_message}` : ""}
              </div>
              <span className="text-muted-foreground">{new Date(d.created_at).toLocaleString()}</span>
            </div>
          ))}
        </CardContent>
      </Card>
    </div>
  );
}
