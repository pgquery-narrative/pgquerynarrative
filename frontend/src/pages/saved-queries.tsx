import { useEffect, useState, useCallback, useRef } from "react";
import { Link } from "react-router-dom";
import { Card, CardHeader, CardTitle, CardContent, CardDescription } from "@/components/ui/card";
import { Button, buttonVariants } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { api, type SavedQuery, type ConnectionInfo, ApiError } from "@/api/client";
import { Search, Trash2, Copy, Play, AlertCircle, Bookmark } from "lucide-react";
import { truncate } from "@/lib/utils";
import { useNavigate } from "react-router-dom";

export default function SavedQueries() {
  const [queries, setQueries] = useState<SavedQuery[]>([]);
  const [loading, setLoading] = useState(true);
  const [search, setSearch] = useState("");
  const [semantic, setSemantic] = useState<{ sql: string; title: string; source: string }[]>([]);
  const [semanticLoading, setSemanticLoading] = useState(false);
  const [error, setError] = useState("");
  const searchDebounce = useRef<ReturnType<typeof setTimeout> | null>(null);
  const [selected, setSelected] = useState<SavedQuery | null>(null);
  const [connections, setConnections] = useState<ConnectionInfo[]>([]);
  const [connectionFilter, setConnectionFilter] = useState("");
  const nav = useNavigate();

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const res = await api.listSaved(100, 0, connectionFilter || undefined);
      setQueries(res.items || []);
    } catch { setError("Failed to load saved queries."); }
    finally { setLoading(false); }
  }, [connectionFilter]);

  useEffect(() => { load(); }, [load]);
  useEffect(() => {
    api.listConnections().then((r) => setConnections(r.items || [])).catch(() => {});
  }, []);

  useEffect(() => {
    const q = search.trim();
    if (!q) {
      setSemantic([]);
      setSemanticLoading(false);
      return;
    }
    setSemanticLoading(true);
    if (searchDebounce.current) clearTimeout(searchDebounce.current);
    searchDebounce.current = setTimeout(() => {
      api.findSimilarQueries(q, 15)
        .then((r) => setSemantic(r.suggestions ?? []))
        .catch(() => setSemantic([]))
        .finally(() => setSemanticLoading(false));
    }, 350);
    return () => {
      if (searchDebounce.current) clearTimeout(searchDebounce.current);
    };
  }, [search]);

  const handleDelete = async (id: string) => {
    if (!confirm("Delete this saved query?")) return;
    try {
      await api.deleteSaved(id);
      setQueries((prev) => prev.filter((q) => q.id !== id));
      if (selected?.id === id) setSelected(null);
    } catch (e) {
      setError(e instanceof ApiError ? e.message : "Delete failed.");
    }
  };

  const filtered = search.trim()
    ? queries.filter(
        (q) => q.name.toLowerCase().includes(search.toLowerCase()) || q.sql.toLowerCase().includes(search.toLowerCase()) || (q.tags || []).some((t) => t.toLowerCase().includes(search.toLowerCase()))
      )
    : queries;

  const showSemantic = search.trim().length > 0;

  const renderQueryCard = (q: SavedQuery, badge?: string) => (
    <div
      key={q.id}
      data-testid="saved-query-item"
      data-query-name={q.name}
      className={`w-full p-4 rounded-lg border transition-colors ${selected?.id === q.id || selected?.sql === q.sql ? "border-primary/50 bg-primary/5" : "border-border hover:bg-secondary/30"}`}
    >
      <button onClick={() => setSelected(q)} className="w-full text-left cursor-pointer">
        <div className="flex items-center justify-between gap-2">
          <p className="text-sm font-medium truncate">{q.name}</p>
          <div className="flex items-center gap-2 shrink-0">
            {badge && <Badge variant="secondary" className="text-[10px]">{badge}</Badge>}
            <Badge variant="secondary" className="text-[10px]">{q.connection_id}</Badge>
            <span className="text-[10px] text-muted-foreground">{new Date(q.created_at).toLocaleDateString()}</span>
          </div>
        </div>
        <p className="text-xs text-muted-foreground font-mono mt-1 truncate">{truncate(q.sql, 80)}</p>
      </button>
      {!q.id.startsWith("semantic-") && (
        <div className="mt-2 flex items-center justify-between gap-2">
          {q.tags && q.tags.length > 0 ? (
            <div className="flex gap-1.5 flex-wrap">{q.tags.map((t) => <Badge key={t} variant="outline" className="text-[10px]">{t}</Badge>)}</div>
          ) : (
            <span className="text-[10px] text-muted-foreground">No tags</span>
          )}
          <Button variant="destructive" size="sm" onClick={() => handleDelete(q.id)} className="h-7 w-7 p-0 rounded-full" title="Delete saved query" aria-label={`Delete saved query ${q.name}`}>
            <Trash2 className="h-3.5 w-3.5" />
          </Button>
        </div>
      )}
    </div>
  );

  return (
    <div data-testid="saved-queries-page" className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold tracking-tight">Saved Queries</h1>
          <p className="text-muted-foreground mt-1">Manage bookmarked SQL. Search uses pgvector similarity when embeddings are enabled.</p>
        </div>
      </div>

      {error && (
        <div
          role="alert"
          className="flex items-start gap-3 p-4 rounded-lg border border-destructive/40 bg-destructive/10 text-destructive text-sm shadow-sm ring-1 ring-destructive/20"
        >
          <AlertCircle className="h-5 w-5 flex-shrink-0" aria-hidden />
          <span className="min-w-0">{error}</span>
        </div>
      )}

      <div className="relative">
        <Search className="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground" />
        <Input placeholder="Search by name/SQL or semantic: revenue by region..." value={search} onChange={(e) => setSearch(e.target.value)} className="pl-10" />
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

      <div className="grid gap-6 md:grid-cols-[1fr_1fr]">
        {/* List */}
        <div data-testid="saved-queries-list" className="space-y-2">
          {loading || semanticLoading ? [1, 2, 3, 4].map((i) => <Skeleton key={i} className="h-20 w-full" />) : showSemantic && semantic.length > 0 ? (
            semantic.map((s, idx) => {
              const match = queries.find((q) => q.sql === s.sql);
              const item = match ?? { id: `semantic-${idx}`, name: s.title, sql: s.sql, connection_id: "default", created_at: new Date().toISOString() } as SavedQuery;
              return renderQueryCard(item, "pgvector");
            })
          ) : filtered.length === 0 ? (
            <Card>
              <CardContent className="py-12 text-center space-y-4">
                <Bookmark className="h-8 w-8 text-muted-foreground mx-auto mb-3" />
                <p className="text-sm text-muted-foreground">{search ? "No queries match. Save queries with embeddings enabled for semantic search." : "No saved queries yet. Save one from the Query Runner."}</p>
                {!search && (
                  <Link to="/query" className={cn(buttonVariants(), "!text-white hover:!text-white dark:!text-black dark:hover:!text-black")}>Go to Query Runner</Link>
                )}
              </CardContent>
            </Card>
          ) : (
            filtered.map((q) => renderQueryCard(q, showSemantic ? "text" : undefined))
          )}
        </div>

        {/* Detail panel */}
        <Card className="h-fit sticky top-8">
          {selected ? (
            <>
              <CardHeader>
                <CardTitle>{selected.name}</CardTitle>
                <CardDescription>Created {new Date(selected.created_at).toLocaleString()}</CardDescription>
              </CardHeader>
              <CardContent className="space-y-4">
                {selected.description && <p className="text-sm text-muted-foreground">{selected.description}</p>}
                <pre className="p-4 rounded-md bg-background border border-border text-xs font-mono overflow-auto max-h-[200px] whitespace-pre-wrap">{selected.sql}</pre>
                {selected.tags && selected.tags.length > 0 && (
                  <div className="flex flex-wrap gap-1.5">{selected.tags.map((t) => <Badge key={t}>{t}</Badge>)}</div>
                )}
                <div className="flex gap-2 pt-2">
                  <Button size="sm" onClick={() => nav("/query", { state: { sql: selected.sql } })}><Play className="h-3.5 w-3.5" /> Run</Button>
                  <Button variant="ghost" size="sm" onClick={() => navigator.clipboard.writeText(selected.sql)}><Copy className="h-3.5 w-3.5" /> Copy SQL</Button>
                  <Button
                    variant="destructive"
                    size="sm"
                    onClick={() => handleDelete(selected.id)}
                    className="ml-auto h-7 w-7 p-0 rounded-full"
                    title="Delete saved query"
                    aria-label={`Delete saved query ${selected.name}`}
                  >
                    <Trash2 className="h-3.5 w-3.5" />
                  </Button>
                </div>
              </CardContent>
            </>
          ) : (
            <CardContent className="py-16 text-center">
              <p className="text-sm text-muted-foreground">Select a query to view details.</p>
            </CardContent>
          )}
        </Card>
      </div>
    </div>
  );
}
