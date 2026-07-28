import type { ReactNode } from "react";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";

export interface InvestigationReportPayload {
  report_type?: string;
  executive_summary?: string;
  impact?: {
    severity?: string;
    summary?: string;
    mean_time_ms?: number;
    total_time_ms?: number;
    calls?: number;
  };
  source_query?: string;
  postgresql_evidence?: string[];
  plan_findings?: Array<{
    category?: string;
    confidence?: string;
    message?: string;
    evidence?: string[];
    investigate?: string[];
  }>;
  candidate_improvements?: Array<{
    proposed_change?: string;
    why_it_might_help?: string;
    confidence?: string;
    required_verification?: string[];
  }>;
  controlled_test_results?: {
    metrics?: Array<{ evidence: string; before: string; after: string; change: string }>;
    improved?: string[];
  };
  equivalence_validation?: {
    status?: string;
    notes?: string;
    checksum_equal?: boolean;
  };
  risks_and_tradeoffs?: string[];
  recommended_next_action?: string;
  provenance?: {
    connection_type?: string;
    query_fingerprint?: string;
    analysis_timestamp?: string;
    explain_analyze?: boolean;
    results_sampled?: boolean;
    generated_by?: string;
  };
}

export function parseInvestigationReport(metrics: Record<string, unknown> | undefined): InvestigationReportPayload | null {
  if (!metrics?.investigation || typeof metrics.investigation !== "object") return null;
  return metrics.investigation as InvestigationReportPayload;
}

/** Engineering-quality Query Investigation report layout. */
export function InvestigationReportView({ report }: { report: InvestigationReportPayload }) {
  return (
    <div className="space-y-6">
      <Card className="panel-accent-top">
        <CardHeader>
          <div className="flex items-center gap-2">
            <Badge variant="outline">Query Investigation Report</Badge>
            {report.impact?.severity && (
              <Badge variant={report.impact.severity === "critical" ? "destructive" : "secondary"} className="capitalize">
                {report.impact.severity} impact
              </Badge>
            )}
          </div>
          <CardTitle className="text-lg mt-2">Executive summary</CardTitle>
        </CardHeader>
        <CardContent className="text-sm leading-relaxed">{report.executive_summary}</CardContent>
      </Card>

      {report.impact?.summary && (
        <Section title="Impact">
          <p className="text-sm">{report.impact.summary}</p>
        </Section>
      )}

      {report.source_query && (
        <Section title="Source query">
          <pre className="text-xs font-mono bg-muted/40 rounded-lg p-4 overflow-x-auto whitespace-pre-wrap">{report.source_query}</pre>
        </Section>
      )}

      {report.postgresql_evidence && report.postgresql_evidence.length > 0 && (
        <Section title="PostgreSQL evidence">
          <ul className="space-y-1 text-sm">{report.postgresql_evidence.map((e, i) => <li key={i}>• {e}</li>)}</ul>
        </Section>
      )}

      {report.plan_findings && report.plan_findings.length > 0 && (
        <Section title="Execution-plan findings">
          <div className="space-y-3">
            {report.plan_findings.map((f, i) => (
              <div key={i} className="rounded-lg border border-border/70 p-3 text-sm">
                <div className="flex items-center gap-2 mb-1">
                  <Badge variant="outline" className="text-[10px]">{f.category || "finding"}</Badge>
                  {f.confidence && <Badge variant="secondary" className="text-[10px] capitalize">Confidence: {f.confidence}</Badge>}
                </div>
                <p>{f.message}</p>
                {f.investigate && f.investigate.length > 0 && (
                  <p className="text-xs text-muted-foreground mt-2">Investigate: {f.investigate.join(" · ")}</p>
                )}
              </div>
            ))}
          </div>
        </Section>
      )}

      {report.candidate_improvements && report.candidate_improvements.length > 0 && (
        <Section title="Candidate improvements">
          <div className="space-y-3">
            {report.candidate_improvements.map((c, i) => (
              <div key={i} className="rounded-lg border border-border/70 p-3 text-sm space-y-2">
                <pre className="text-xs font-mono bg-muted/30 rounded p-2 overflow-x-auto whitespace-pre-wrap">{c.proposed_change}</pre>
                <p>{c.why_it_might_help}</p>
                {c.confidence && <Badge variant="secondary" className="text-[10px] capitalize">Confidence: {c.confidence}</Badge>}
                {c.required_verification && (
                  <ul className="text-xs text-muted-foreground">{c.required_verification.map((v) => <li key={v}>• {v}</li>)}</ul>
                )}
              </div>
            ))}
          </div>
        </Section>
      )}

      {report.controlled_test_results?.metrics && report.controlled_test_results.metrics.length > 0 && (
        <Section title="Controlled test results">
          <div className="overflow-x-auto rounded-lg border border-border/70">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b bg-muted/30 text-left text-xs">
                  <th className="p-2">Evidence</th><th className="p-2">Before</th><th className="p-2">After</th><th className="p-2">Change</th>
                </tr>
              </thead>
              <tbody>
                {report.controlled_test_results.metrics.map((m) => (
                  <tr key={m.evidence} className="border-b border-border/30 last:border-0">
                    <td className="p-2 font-medium">{m.evidence}</td>
                    <td className="p-2 font-mono text-muted-foreground">{m.before}</td>
                    <td className="p-2 font-mono">{m.after}</td>
                    <td className="p-2">{m.change}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Section>
      )}

      {report.equivalence_validation && (
        <Section title="Result-equivalence validation">
          <p className="text-sm"><strong>Status:</strong> {report.equivalence_validation.status}</p>
          <p className="text-sm text-muted-foreground mt-1">{report.equivalence_validation.notes}</p>
        </Section>
      )}

      {report.risks_and_tradeoffs && report.risks_and_tradeoffs.length > 0 && (
        <Section title="Risks and tradeoffs">
          <ul className="text-sm space-y-1">{report.risks_and_tradeoffs.map((r) => <li key={r}>• {r}</li>)}</ul>
        </Section>
      )}

      {report.recommended_next_action && (
        <Section title="Recommended next action">
          <p className="text-sm font-medium">{report.recommended_next_action}</p>
        </Section>
      )}

      {report.provenance && (
        <Section title="Environment and provenance">
          <dl className="grid grid-cols-2 gap-2 text-xs">
            {report.provenance.connection_type && <><dt className="text-muted-foreground">Connection</dt><dd>{report.provenance.connection_type}</dd></>}
            {report.provenance.query_fingerprint && <><dt className="text-muted-foreground">Query hash</dt><dd className="font-mono">{report.provenance.query_fingerprint}</dd></>}
            {report.provenance.analysis_timestamp && <><dt className="text-muted-foreground">Analysis time</dt><dd>{new Date(report.provenance.analysis_timestamp).toLocaleString()}</dd></>}
            {report.provenance.generated_by && <><dt className="text-muted-foreground">Generated by</dt><dd>{report.provenance.generated_by}</dd></>}
          </dl>
        </Section>
      )}
    </div>
  );
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <Card>
      <CardHeader className="pb-2"><CardTitle className="text-base">{title}</CardTitle></CardHeader>
      <CardContent>{children}</CardContent>
    </Card>
  );
}
