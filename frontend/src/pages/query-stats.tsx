import { useCallback, useEffect, useState } from "react";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { api, type StatStatementRow, ApiError } from "@/api/client";
import { RefreshCw, AlertCircle } from "lucide-react";
import { cn, formatFloat } from "@/lib/utils";

type OrderBy = "total_time" | "mean_time" | "calls";

const ORDER_OPTIONS: { value: OrderBy; label: string }[] = [
  { value: "total_time", label: "Total time" },
  { value: "mean_time", label: "Mean time" },
  { value: "calls", label: "Calls" },
];

export default function QueryStats() {
  const [orderBy, setOrderBy] = useState<OrderBy>("total_time");
  const [limit, setLimit] = useState(20);
  const [items, setItems] = useState<StatStatementRow[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const res = await api.listStatStatements(orderBy, limit);
      setItems(res.items ?? []);
    } catch (e) {
      const msg = e instanceof ApiError ? e.message : "Failed to load query stats";
      setError(msg);
      setItems([]);
    } finally {
      setLoading(false);
    }
  }, [orderBy, limit]);

  useEffect(() => {
    void load();
  }, [load]);

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold tracking-tight">Query stats</h1>
        <p className="text-muted-foreground mt-1">
          Top statements from <code className="text-xs">pg_stat_statements</code> on the read-only connection.
        </p>
      </div>

      <Card>
        <CardHeader className="pb-3">
          <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-3">
            <div>
              <CardTitle className="text-base">Ranked statements</CardTitle>
              <CardDescription>Requires extension enabled and Postgres restarted with shared_preload_libraries.</CardDescription>
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <div className="flex rounded-lg border border-border overflow-hidden">
                {ORDER_OPTIONS.map((opt) => (
                  <button
                    key={opt.value}
                    type="button"
                    onClick={() => setOrderBy(opt.value)}
                    className={cn(
                      "px-3 py-1.5 text-xs font-medium transition-colors",
                      orderBy === opt.value
                        ? "bg-primary text-primary-foreground"
                        : "bg-muted/40 text-muted-foreground hover:text-foreground"
                    )}
                  >
                    {opt.label}
                  </button>
                ))}
              </div>
              <select
                aria-label="Result limit"
                value={limit}
                onChange={(e) => setLimit(Number(e.target.value))}
                className="h-8 rounded-md border border-border bg-background px-2 text-xs"
              >
                {[10, 20, 50].map((n) => (
                  <option key={n} value={n}>
                    Top {n}
                  </option>
                ))}
              </select>
              <Button type="button" variant="outline" size="sm" onClick={() => void load()} disabled={loading}>
                <RefreshCw className={cn("h-3.5 w-3.5 mr-1.5", loading && "animate-spin")} />
                Refresh
              </Button>
            </div>
          </div>
        </CardHeader>
        <CardContent>
          {error && (
            <div className="mb-4 flex items-start gap-2 rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
              <AlertCircle className="h-4 w-4 shrink-0 mt-0.5" />
              <span>{error}</span>
            </div>
          )}
          {loading ? (
            <div className="space-y-2">
              {Array.from({ length: 5 }).map((_, i) => (
                <Skeleton key={i} className="h-10 w-full" />
              ))}
            </div>
          ) : items.length === 0 ? (
            <p className="text-sm text-muted-foreground">No statements recorded yet. Run queries via Query Runner, then refresh.</p>
          ) : (
            <div className="overflow-x-auto rounded-lg border border-border">
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b border-border bg-muted/40 text-left text-xs text-muted-foreground">
                    <th className="px-3 py-2 font-medium">Query</th>
                    <th className="px-3 py-2 font-medium text-right whitespace-nowrap">Calls</th>
                    <th className="px-3 py-2 font-medium text-right whitespace-nowrap">Total ms</th>
                    <th className="px-3 py-2 font-medium text-right whitespace-nowrap">Mean ms</th>
                    <th className="px-3 py-2 font-medium text-right whitespace-nowrap">Rows</th>
                  </tr>
                </thead>
                <tbody>
                  {items.map((row, idx) => (
                    <tr key={row.queryid ?? idx} className="border-b border-border/60 last:border-0">
                      <td className="px-3 py-2 font-mono text-xs max-w-xl truncate" title={row.query}>
                        {row.query}
                      </td>
                      <td className="px-3 py-2 text-right tabular-nums">{row.calls.toLocaleString()}</td>
                      <td className="px-3 py-2 text-right tabular-nums">{formatFloat(row.total_time_ms)}</td>
                      <td className="px-3 py-2 text-right tabular-nums">{formatFloat(row.mean_time_ms)}</td>
                      <td className="px-3 py-2 text-right tabular-nums">{row.rows.toLocaleString()}</td>
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
