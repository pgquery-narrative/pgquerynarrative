import { cn } from "@/lib/utils";
import { Shield, Database, Clock, Rows3, Lock } from "lucide-react";

export interface TrustBarProps {
  connectionName?: string;
  schemas?: string[];
  timeoutSeconds?: number;
  resultLimit?: number;
  className?: string;
}

/** Permanent trust bar showing connection safety posture. */
export function TrustBar({
  connectionName = "Analytics Replica",
  schemas = ["demo"],
  timeoutSeconds = 30,
  resultLimit = 10000,
  className,
}: TrustBarProps) {
  const items = [
    { icon: Database, label: "Connection", value: connectionName },
    { icon: Lock, label: "Access", value: "Read-only" },
    { icon: Shield, label: "Schemas", value: schemas.join(", ") },
    { icon: Clock, label: "Timeout", value: `${timeoutSeconds} seconds` },
    { icon: Rows3, label: "Result limit", value: `${resultLimit.toLocaleString()} rows` },
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
