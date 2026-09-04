import { cn } from "@/lib/utils";
import type { SecurityTrust } from "@/api/client";
import { Shield, Database, Clock, Rows3, Lock } from "lucide-react";

export interface TrustBarProps {
  connectionName?: string;
  /** Real posture for the active connection, from GET /trust. Undefined while
   * loading — the bar shows "—" rather than a guessed number in that state. */
  trust?: SecurityTrust | null;
  className?: string;
}

const UNKNOWN = "—";

/**
 * Permanent trust bar showing the active connection's real, live safety
 * posture. Every value comes from the Security & Trust API response for the
 * resolved connection — there is no hardcoded fallback, so a misconfigured or
 * insecure connection shows as such instead of a plausible-looking default.
 */
export function TrustBar({ connectionName, trust, className }: TrustBarProps) {
  const items = [
    { icon: Database, label: "Connection", value: connectionName || trust?.connection_id || UNKNOWN },
    {
      icon: Lock,
      label: "Access",
      value: !trust ? UNKNOWN : trust.readonly ? "Read-only (confirmed)" : "Not confirmed read-only",
    },
    { icon: Shield, label: "Schemas", value: trust?.allowed_schemas?.length ? trust.allowed_schemas.join(", ") : UNKNOWN },
    {
      icon: Clock,
      label: "Timeout",
      value: trust ? (trust.query_timeout_seconds > 0 ? `${trust.query_timeout_seconds} seconds` : "none enforced") : UNKNOWN,
    },
    { icon: Rows3, label: "Result limit", value: trust ? `${trust.result_limit.toLocaleString()} rows` : UNKNOWN },
    { icon: Lock, label: "TLS", value: trust?.tls || UNKNOWN },
  ];

  return (
    <div
      className={cn(
        "flex flex-wrap items-center gap-x-6 gap-y-2 rounded-lg border border-border/70 bg-muted/30 px-4 py-2.5 text-xs",
        className
      )}
      role="status"
      aria-label="Connection trust and safety settings"
    >
      {items.map(({ icon: Icon, label, value }) => (
        <div key={label} className="flex items-center gap-2 text-muted-foreground">
          <Icon className="h-3.5 w-3.5 shrink-0 text-primary/80" aria-hidden />
          <span className="font-medium text-foreground/80">{label}:</span>
          <span>{value}</span>
        </div>
      ))}
    </div>
  );
}
