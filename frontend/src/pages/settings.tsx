import { useEffect, useState } from "react";
import { Card, CardHeader, CardTitle, CardContent, CardDescription } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Database, Cpu, Settings2, BarChart3 } from "lucide-react";
import { api, type AnalyticsSettings, type EmbeddingSettings, type LLMSettings } from "@/api/client";
import { getApiKey, setApiKey } from "@/api/auth";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";

export default function SettingsPage() {
  const [analytics, setAnalytics] = useState<AnalyticsSettings | null>(null);
  const [llm, setLlm] = useState<LLMSettings | null>(null);
  const [embedding, setEmbedding] = useState<EmbeddingSettings | null>(null);
  const [authEnabled, setAuthEnabled] = useState(false);
  const [apiKey, setApiKeyState] = useState(getApiKey());
  const [apiKeySaved, setApiKeySaved] = useState(false);

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
  }, []);

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
            {authEnabled && (
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
                <p className="text-xs text-muted-foreground">Stored in this browser only (localStorage).</p>
              </div>
            )}
            <Row label="Allowed schemas" value="demo (default)" />
            <Row label="Max query length" value="10,000 chars" />
            <Row label="Max rows per query" value="1,000" />
            <Row label="Server port" value={envOrDefault("PORT", "8080")} />
          </CardContent>
        </Card>
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
