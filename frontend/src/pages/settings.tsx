import { useCallback, useEffect, useState } from "react";
import { Card, CardHeader, CardTitle, CardContent, CardDescription } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Database, Cpu, Settings2, BarChart3, KeyRound, Trash2 } from "lucide-react";
import { api, type AnalyticsSettings, type EmbeddingSettings, type LLMSettings, type ManagedAPIKey } from "@/api/client";
import { fetchSessionStatus, getApiKey, setApiKey, isBrowserKeyStorageAllowed } from "@/api/auth";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";

export default function SettingsPage() {
  const [analytics, setAnalytics] = useState<AnalyticsSettings | null>(null);
  const [llm, setLlm] = useState<LLMSettings | null>(null);
  const [embedding, setEmbedding] = useState<EmbeddingSettings | null>(null);
  const [authEnabled, setAuthEnabled] = useState(false);
  const [apiKey, setApiKeyState] = useState(getApiKey());
  const [apiKeySaved, setApiKeySaved] = useState(false);
  const [sessionRole, setSessionRole] = useState<string | undefined>();
  const [managedKeys, setManagedKeys] = useState<ManagedAPIKey[]>([]);
  const [managedKeysLoading, setManagedKeysLoading] = useState(false);
  const [managedKeyRole, setManagedKeyRole] = useState("analyst");
  const [managedKeyMsg, setManagedKeyMsg] = useState("");
  const [issuedSecret, setIssuedSecret] = useState("");
  const [creatingKey, setCreatingKey] = useState(false);
  const [revokingKeyId, setRevokingKeyId] = useState<string | null>(null);

  const isAdmin = sessionRole === "admin";

  const refreshManagedKeys = useCallback(async () => {
    if (!isAdmin) return;
    setManagedKeysLoading(true);
    try {
      const res = await api.listManagedKeys();
      setManagedKeys(res.items ?? []);
    } catch {
      setManagedKeys([]);
    } finally {
      setManagedKeysLoading(false);
    }
  }, [isAdmin]);

  useEffect(() => {
    api
      .getSettings()
      .then((r) => {
        setAnalytics(r.analytics);
        if (r.llm) setLlm(r.llm);
        if (r.embedding) setEmbedding(r.embedding);
        if (r.security) setAuthEnabled(r.security.auth_enabled);
      })
      .catch(() => {});
    fetchSessionStatus()
      .then((s) => {
        if (s.authenticated) setSessionRole(s.role);
      })
      .catch(() => {});
  }, []);

  useEffect(() => {
    void refreshManagedKeys();
  }, [refreshManagedKeys]);

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold tracking-tight">Settings</h1>
        <p className="text-muted-foreground mt-1">Server configuration (read-only). Change via environment variables.</p>
      </div>

      <div className="grid gap-6 md:grid-cols-2">
        <Card>
          <CardHeader className="flex flex-row items-center gap-3">
            <Database className="h-5 w-5 text-brand-blue" />
            <div>
              <CardTitle>Database</CardTitle>
              <CardDescription>PostgreSQL connection</CardDescription>
            </div>
          </CardHeader>
          <CardContent className="space-y-3 text-sm">
            <Row label="Host" value={envOrDefault("DB_HOST", "localhost")} />
            <Row label="Port" value={envOrDefault("DB_PORT", "5432")} />
            <Row label="Database" value={envOrDefault("DB_NAME", "pgquerynarrative")} />
            <Row label="Read-only user" value={envOrDefault("DB_READONLY_USER", "pgquerynarrative_readonly")} />
            <Row label="SSL mode" value={envOrDefault("DB_SSL_MODE", "disable")} />
          </CardContent>
        </Card>

        <Card>
          <CardHeader className="flex flex-row items-center gap-3">
            <Cpu className="h-5 w-5 text-brand-indigo" />
            <div>
              <CardTitle>LLM Provider</CardTitle>
              <CardDescription>Narrative generation model</CardDescription>
            </div>
          </CardHeader>
          <CardContent className="space-y-3 text-sm">
            {llm ? (
              <>
                <Row label="Provider" value={llm.provider} />
                <Row label="Model" value={llm.model} />
                <Row label="Base URL" value={llm.base_url || "—"} />
                <Row label="API key" value={llm.api_key_configured ? "set" : "—"} masked={llm.api_key_configured} />
              </>
            ) : (
              <>
                <Row label="Provider" value="ollama" />
                <Row label="Model" value="llama3.2" />
                <Row label="Base URL" value="http://localhost:11434" />
                <Row label="API key" value="—" />
              </>
            )}
            {embedding && (
              <>
                <Row label="Embeddings" value={embedding.enabled ? "on" : "off"} title="Similar queries / RAG when on" />
                {embedding.enabled && (
                  <>
                    <Row label="Embedding model" value={embedding.model || "—"} />
                    <Row label="Embedding URL" value={embedding.base_url || "—"} />
                  </>
                )}
              </>
            )}
          </CardContent>
        </Card>

        {analytics && (
          <Card>
            <CardHeader className="flex flex-row items-center gap-3">
              <BarChart3 className="h-5 w-5 text-brand-blue" />
              <div>
                <CardTitle>Analytics</CardTitle>
                <CardDescription>Time-series and anomaly detection windows</CardDescription>
              </div>
            </CardHeader>
            <CardContent className="space-y-3 text-sm">
              <Row label="Anomaly sigma (σ)" value={String(analytics.anomaly_sigma)} title="Z-score threshold for anomaly detection (1–5)" />
              <Row label="Trend periods" value={String(analytics.trend_periods)} title="Periods used for linear regression (2–24)" />
              <Row label="Moving avg window" value={String(analytics.moving_avg_window)} title="Simple moving average length (2–24)" />
              <Row label="Trend threshold %" value={String(analytics.trend_threshold_percent)} title="Min % change for up/down vs flat" />
              <Row label="Forecast confidence" value={String(analytics.confidence_level)} title="Confidence level for forecast interval (e.g. 0.95)" />
              {analytics.anomaly_method != null && <Row label="Anomaly method" value={String(analytics.anomaly_method)} title="zscore or isolation_forest" />}
              {analytics.min_rows_for_correlation != null && <Row label="Correlation min rows" value={String(analytics.min_rows_for_correlation)} title="Min rows for Pearson/Spearman" />}
              {analytics.smoothing_alpha != null && <Row label="Smoothing α" value={String(analytics.smoothing_alpha)} title="Exponential smoothing level" />}
              {analytics.smoothing_beta != null && <Row label="Smoothing β" value={String(analytics.smoothing_beta)} title="Holt trend smoothing" />}
              {analytics.max_seasonal_lag != null && <Row label="Max seasonal lag" value={String(analytics.max_seasonal_lag)} title="Max period for seasonality" />}
              {analytics.min_periods_for_seasonality != null && <Row label="Min periods (seasonality)" value={String(analytics.min_periods_for_seasonality)} title="Min series length for seasonality" />}
              {analytics.max_timeseries_periods != null && <Row label="Max time-series periods" value={String(analytics.max_timeseries_periods)} title="Max periods in time-series (last N for charts)" />}
            </CardContent>
          </Card>
        )}

        <Card className="md:col-span-2">
          <CardHeader className="flex flex-row items-center gap-3">
            <Settings2 className="h-5 w-5 text-muted-foreground" />
            <div>
              <CardTitle>Application</CardTitle>
              <CardDescription>General settings</CardDescription>
            </div>
          </CardHeader>
          <CardContent className="space-y-3 text-sm">
            <Row label="Auth enabled" value={authEnabled ? "yes" : "no"} />
            {authEnabled && isBrowserKeyStorageAllowed() && (
              <div className="space-y-2 pt-2 border-t border-border/50">
                <label htmlFor="api-key" className="text-sm text-muted-foreground">API key (Bearer token)</label>
                <Input
                  id="api-key"
                  type="password"
                  placeholder="Paste SECURITY_API_KEY"
                  value={apiKey}
                  onChange={(e) => setApiKeyState(e.target.value)}
                />
                <Button
                  type="button"
                  size="sm"
                  onClick={() => {
                    setApiKey(apiKey);
                    setApiKeySaved(true);
                    setTimeout(() => setApiKeySaved(false), 2000);
                  }}
                >
                  {apiKeySaved ? "Saved" : "Save API key"}
                </Button>
                <p className="text-xs text-muted-foreground">Stored in this browser only (localStorage). Enabled via VITE_ALLOW_BROWSER_API_KEY for this deployment.</p>
              </div>
            )}
            {authEnabled && !isBrowserKeyStorageAllowed() && (
              <p className="text-xs text-muted-foreground pt-2 border-t border-border/50">
                Use a session login or the CLI to authenticate; storing API keys in the browser is disabled in this deployment.
              </p>
            )}
            <Row label="Session role" value={sessionRole || "—"} />
            <Row label="Allowed schemas" value="demo (default)" />
            <Row label="Max query length" value="10,000 chars" />
            <Row label="Max rows per query" value="1,000" />
            <Row label="Server port" value={envOrDefault("PORT", "8080")} />
          </CardContent>
        </Card>

        {isAdmin && (
          <Card className="md:col-span-2">
            <CardHeader className="flex flex-row items-center gap-3">
              <KeyRound className="h-5 w-5 text-brand-indigo" />
              <div>
                <CardTitle>Managed API keys</CardTitle>
                <CardDescription>Org-scoped keys issued by admins. The secret is shown once at creation.</CardDescription>
              </div>
            </CardHeader>
            <CardContent className="space-y-4 text-sm">
              <div className="flex flex-wrap gap-2 items-center">
                <select
                  className="h-10 rounded-md border border-input bg-background px-3 text-sm"
                  value={managedKeyRole}
                  onChange={(e) => setManagedKeyRole(e.target.value)}
                >
                  <option value="viewer">viewer</option>
                  <option value="analyst">analyst</option>
                  <option value="admin">admin</option>
                </select>
                <Button
                  type="button"
                  size="sm"
                  disabled={creatingKey}
                  onClick={() => {
                    void (async () => {
                      setCreatingKey(true);
                      setManagedKeyMsg("");
                      setIssuedSecret("");
                      try {
                        const issued = await api.createManagedKey({ role: managedKeyRole });
                        setIssuedSecret(issued.secret);
                        setManagedKeyMsg(`Created key ${issued.prefix}… — copy the secret now`);
                        await refreshManagedKeys();
                      } catch (e) {
                        setManagedKeyMsg(e instanceof Error ? e.message : "Failed to create key");
                      } finally {
                        setCreatingKey(false);
                      }
                    })();
                  }}
                >
                  {creatingKey ? "Creating…" : "Create key"}
                </Button>
                {managedKeyMsg && <p className="text-xs text-muted-foreground">{managedKeyMsg}</p>}
              </div>
              {issuedSecret && (
                <div className="rounded-md border border-border bg-muted/40 p-3 space-y-2">
                  <p className="text-xs font-medium">New secret (shown once)</p>
                  <code className="block text-xs break-all font-mono">{issuedSecret}</code>
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    onClick={() => {
                      void navigator.clipboard.writeText(issuedSecret);
                    }}
                  >
                    Copy secret
                  </Button>
                </div>
              )}
              {managedKeysLoading ? (
                <p className="text-xs text-muted-foreground">Loading keys…</p>
              ) : managedKeys.length === 0 ? (
                <p className="text-xs text-muted-foreground">No managed keys yet.</p>
              ) : (
                <ul className="space-y-2">
                  {managedKeys.map((k) => {
                    const revoked = Boolean(k.revoked_at);
                    return (
                      <li key={k.id} className="flex flex-wrap items-center justify-between gap-2 border border-border rounded-md px-3 py-2">
                        <div className="space-y-0.5">
                          <p className="font-mono text-xs">{k.prefix}… · {k.role}</p>
                          <p className="text-xs text-muted-foreground">
                            created {new Date(k.created_at).toLocaleString()}
                            {k.expires_at ? ` · expires ${new Date(k.expires_at).toLocaleString()}` : ""}
                            {revoked ? ` · revoked ${new Date(k.revoked_at!).toLocaleString()}` : ""}
                          </p>
                        </div>
                        {!revoked ? (
                          <Button
                            type="button"
                            variant="destructive"
                            size="sm"
                            disabled={revokingKeyId === k.id}
                            onClick={() => {
                              void (async () => {
                                setRevokingKeyId(k.id);
                                try {
                                  await api.revokeManagedKey(k.id);
                                  await refreshManagedKeys();
                                  setManagedKeyMsg("Key revoked");
                                } catch (e) {
                                  setManagedKeyMsg(e instanceof Error ? e.message : "Failed to revoke key");
                                } finally {
                                  setRevokingKeyId(null);
                                }
                              })();
                            }}
                          >
                            <Trash2 className="h-3.5 w-3.5" />
                            Revoke
                          </Button>
                        ) : (
                          <Badge variant="secondary">revoked</Badge>
                        )}
                      </li>
                    );
                  })}
                </ul>
              )}
            </CardContent>
          </Card>
        )}
      </div>
    </div>
  );
}

function Row({ label, value, masked, title }: { label: string; value: string; masked?: boolean; title?: string }) {
  return (
    <div className="flex items-center justify-between py-1.5 border-b border-border/50 last:border-0" title={title}>
      <span className="text-muted-foreground">{label}</span>
      {masked && value !== "—" ? (
        <Badge variant="secondary">••••••</Badge>
      ) : (
        <span className="font-mono text-xs">{value}</span>
      )}
    </div>
  );
}

function envOrDefault(key: string, fallback: string): string {
  return fallback;
}
