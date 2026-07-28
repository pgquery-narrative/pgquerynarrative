import { useEffect, useState } from "react";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { api, type SecurityTrust } from "@/api/client";
import { Shield, Lock, Database, Clock, Eye, Cloud } from "lucide-react";

export default function SecurityPage() {
  const [trust, setTrust] = useState<SecurityTrust | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    api.getSecurityTrust()
      .then(setTrust)
      .catch(() => {})
      .finally(() => setLoading(false));
  }, []);

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

      {loading ? (
        <div className="grid gap-4 md:grid-cols-2">
          {[1, 2, 3, 4].map((i) => <Skeleton key={i} className="h-24 w-full" />)}
        </div>
      ) : trust ? (
        <div className="grid gap-4 md:grid-cols-2">
          <TrustRow icon={Lock} label="Authentication" value={trust.authentication} status={trust.authentication === "Enabled"} />
          <TrustRow icon={Database} label="Connection mode" value={trust.connection_mode} status />
          <TrustRow icon={Database} label="Allowed schemas" value={trust.allowed_schemas.join(", ")} status />
          <TrustRow icon={Shield} label="Tenant isolation" value={trust.tenant_isolation} status />
          <TrustRow icon={Lock} label="TLS" value={trust.tls} status={trust.tls !== "disable"} />
          <TrustRow icon={Eye} label="Audit mode" value={trust.audit_mode} status={trust.audit_mode === "required"} />
          <TrustRow icon={Clock} label="Query timeout" value={`${trust.query_timeout_seconds} seconds`} status />
          <TrustRow icon={Database} label="Result limit" value={`${trust.result_limit.toLocaleString()} rows`} status />
          <TrustRow icon={Eye} label="EXPLAIN ANALYZE" value={trust.explain_analyze} status={trust.explain_analyze === "Disabled"} />
          <TrustRow icon={Cloud} label="External LLM data" value={trust.external_llm_data} status={trust.external_llm_data === "Disabled"} />
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
