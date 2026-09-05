import { useEffect, useState } from "react";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { api, type SecurityTrust, type ConnectionInfo } from "@/api/client";
import { Shield, Lock, Database, Clock, Eye, Cloud, KeyRound, History } from "lucide-react";

export default function SecurityPage() {
  const [trust, setTrust] = useState<SecurityTrust | null>(null);
  const [loading, setLoading] = useState(true);
  const [connections, setConnections] = useState<ConnectionInfo[]>([]);
  const [connectionId, setConnectionId] = useState<string>("");

  useEffect(() => {
    api.listConnections().then((r) => {
      const items = r.items ?? [];
      setConnections(items);
      setConnectionId((prev) => prev || items[0]?.id || "");
    }).catch(() => {});
  }, []);

  useEffect(() => {
    setLoading(true);
    api.getSecurityTrust(connectionId || undefined)
      .then(setTrust)
      .catch(() => setTrust(null))
      .finally(() => setLoading(false));
  }, [connectionId]);

  return (
    <div className="space-y-8">
      <div>
        <div className="flex items-center gap-2 mb-2">
          <Shield className="h-6 w-6 text-primary" />
          <h1 className="text-2xl font-bold tracking-tight">Security & Trust</h1>
        </div>
        <p className="text-muted-foreground max-w-2xl">
          PgQueryNarrative is designed for read-only PostgreSQL investigation.
          This page shows the active security posture — nothing is hidden behind the interface.
        </p>
      </div>

      {connections.length > 1 && (
        <label className="flex items-center gap-2 text-sm text-muted-foreground">
          Connection
          <select
            className="h-9 rounded-md border border-input bg-background px-3 text-sm text-foreground"
            value={connectionId}
            onChange={(e) => setConnectionId(e.target.value)}
          >
            {connections.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
          </select>
        </label>
      )}

      {loading ? (
        <div className="grid gap-4 md:grid-cols-2">
          {[1, 2, 3, 4].map((i) => <Skeleton key={i} className="h-24 w-full" />)}
        </div>
      ) : trust ? (
        <div className="grid gap-4 md:grid-cols-2">
          <TrustRow icon={Lock} label="Authentication" value={trust.authentication} status={trust.authentication === "Enabled"} />
          <TrustRow icon={Database} label="Connection mode" value={trust.connection_mode} status />
          <TrustRow icon={Lock} label="Read-only (live probe)" value={trust.readonly ? "Confirmed" : "Not confirmed"} status={trust.readonly} />
          <TrustRow icon={Database} label="Allowed schemas" value={trust.allowed_schemas.length ? trust.allowed_schemas.join(", ") : "none"} status={trust.allowed_schemas.length > 0} />
          <TrustRow icon={Shield} label="Tenant isolation" value={trust.tenant_isolation} status />
          <TrustRow icon={Lock} label="TLS" value={trust.tls} status={trust.tls !== "disable" && trust.tls !== "unknown"} />
          <TrustRow icon={Eye} label="Audit mode" value={trust.audit_mode} status={trust.audit_mode === "required"} />
          <TrustRow icon={Clock} label="Query timeout" value={trust.query_timeout_seconds > 0 ? `${trust.query_timeout_seconds} seconds` : "none enforced"} status={trust.query_timeout_seconds > 0} />
          <TrustRow icon={Database} label="Result limit" value={`${trust.result_limit.toLocaleString()} rows`} status={trust.result_limit > 0} />
          <TrustRow icon={Eye} label="EXPLAIN ANALYZE" value={trust.explain_analyze} status={trust.explain_analyze === "Disabled"} />
          <TrustRow icon={KeyRound} label="Analyze policy (you)" value={trust.analyze_policy} status={trust.analyze_policy !== "Disabled (no permission)"} />
          <TrustRow icon={Cloud} label="External LLM data" value={trust.external_llm_data} status={trust.external_llm_data === "Disabled"} />
          <TrustRow
            icon={KeyRound}
            label="Your permissions"
            value={trust.authorization_state.length ? trust.authorization_state.join(", ") : "none"}
            status={trust.authorization_state.length > 0}
          />
          <TrustRow
            icon={History}
            label="Last security verification"
            value={trust.last_security_verification ?? "never"}
            status={!!trust.last_security_verification}
          />
        </div>
      ) : (
        <p className="text-muted-foreground">Unable to load security configuration.</p>
      )}

      <Card>
        <CardHeader>
          <CardTitle className="text-base">What PgQueryNarrative will never do automatically</CardTitle>
          <CardDescription>Core safety guarantees</CardDescription>
        </CardHeader>
        <CardContent>
          <ul className="space-y-2 text-sm text-muted-foreground">
            <li className="flex gap-2"><span className="text-primary">•</span>Modify production data (INSERT, UPDATE, DELETE, DDL)</li>
            <li className="flex gap-2"><span className="text-primary">•</span>Create indexes or run ANALYZE without explicit human action</li>
            <li className="flex gap-2"><span className="text-primary">•</span>Send query results to external LLM providers without opt-in configuration</li>
            <li className="flex gap-2"><span className="text-primary">•</span>Execute queries outside allowed schemas or beyond configured timeouts</li>
            <li className="flex gap-2"><span className="text-primary">•</span>Claim a single certain fix — all recommendations require verification</li>
          </ul>
        </CardContent>
      </Card>
    </div>
  );
}

function TrustRow({
  icon: Icon,
  label,
  value,
  status = true,
}: {
  icon: React.ComponentType<{ className?: string }>;
  label: string;
  value: string;
  status?: boolean;
}) {
  return (
    <Card>
      <CardContent className="p-4 flex items-center justify-between gap-4">
        <div className="flex items-center gap-3 min-w-0">
          <Icon className="h-5 w-5 text-primary shrink-0" />
          <div className="min-w-0">
            <p className="text-xs text-muted-foreground">{label}</p>
            <p className="font-medium text-sm truncate">{value}</p>
          </div>
        </div>
        <Badge variant={status ? "success" : "warning"} className="shrink-0 text-[10px]">
          {status ? "Active" : "Review"}
        </Badge>
      </CardContent>
    </Card>
  );
}
