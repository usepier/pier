import type { ReactNode } from "react";

/** A titled group of related information, the cockpit's building block. */
export function Panel({
  title,
  actions,
  children,
  className = "",
}: {
  title: ReactNode;
  actions?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <section
      className={`flex min-w-0 flex-col rounded-card border border-separator bg-raised/60 ${className}`}
    >
      <header className="flex h-9 shrink-0 items-center gap-2 px-3">
        <h2 className="min-w-0 flex-1 truncate text-sm font-semibold text-label">{title}</h2>
        {actions && <div className="flex shrink-0 items-center gap-1">{actions}</div>}
      </header>
      <div className="min-h-0 flex-1 px-3 pb-3">{children}</div>
    </section>
  );
}

/** A big number with its label: totals, costs. */
export function Stat({ label, value, hint }: { label: string; value: ReactNode; hint?: ReactNode }) {
  return (
    <div className="min-w-0">
      <div className="text-xs text-secondary">{label}</div>
      <div className="text-lg font-semibold tabular-nums text-label">{value}</div>
      {hint && <div className="truncate text-xs text-tertiary">{hint}</div>}
    </div>
  );
}

/** Label/value rows. */
export function Facts({ rows }: { rows: [string, ReactNode][] }) {
  return (
    <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1 text-sm">
      {rows.map(([k, v]) => (
        <div key={k} className="contents">
          <dt className="text-secondary">{k}</dt>
          <dd className="selectable min-w-0 break-words text-label">{v}</dd>
        </div>
      ))}
    </dl>
  );
}

/** A horizontal fill bar (quota, disk). */
export function Meter({
  value,
  max,
  tone = "accent",
}: {
  value: number;
  max: number;
  tone?: "accent" | "warn" | "bad";
}) {
  const pct = max > 0 ? Math.min(100, (value / max) * 100) : 0;
  const color = tone === "bad" ? "bg-bad" : tone === "warn" ? "bg-warn" : "bg-accent";
  return (
    <div className="h-1.5 w-full overflow-hidden rounded-full bg-selected" aria-hidden="true">
      <div className={`h-full rounded-full ${color}`} style={{ width: `${pct}%` }} />
    </div>
  );
}
