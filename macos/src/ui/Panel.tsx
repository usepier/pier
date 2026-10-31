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
      <header className="flex h-12 shrink-0 items-center gap-2 px-5 pt-1">
        <h2 className="min-w-0 flex-1 truncate text-md font-semibold text-label">{title}</h2>
        {actions && <div className="flex shrink-0 items-center gap-1.5">{actions}</div>}
      </header>
      <div className="min-h-0 flex-1 px-5 pt-1 pb-5 text-sm leading-relaxed">{children}</div>
    </section>
  );
}

/** A big number with its label: totals, costs. */
export function Stat({ label, value, hint }: { label: string; value: ReactNode; hint?: ReactNode }) {
  return (
    <div className="min-w-0">
      <div className="text-sm text-secondary">{label}</div>
      <div className="mt-1 text-xl font-semibold tabular-nums text-label">{value}</div>
      {hint && <div className="mt-1 truncate text-xs text-tertiary">{hint}</div>}
    </div>
  );
}

/** A big number in a card of its own: one tile of a grid of them. */
export function StatCard({
  label,
  value,
  hint,
  children,
  title,
}: {
  label: string;
  value: ReactNode;
  hint?: ReactNode;
  /** Below the number: a meter, a graph. */
  children?: ReactNode;
  title?: string;
}) {
  return (
    <div
      title={title}
      className="flex min-h-[108px] min-w-0 flex-col rounded-card border border-separator bg-raised/60 px-4 pt-3.5 pb-4"
    >
      <div className="text-sm text-secondary">{label}</div>
      <div className="mt-1.5 truncate text-xl font-semibold tabular-nums text-label">{value}</div>
      <span className="flex-1" />
      {children && <div className="mt-2.5">{children}</div>}
      {hint && <div className="mt-1.5 truncate text-xs text-tertiary">{hint}</div>}
    </div>
  );
}

/** Label/value rows. */
export function Facts({ rows }: { rows: [string, ReactNode][] }) {
  return (
    <dl className="grid grid-cols-[max-content_1fr] gap-x-8 gap-y-2.5 text-sm">
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
