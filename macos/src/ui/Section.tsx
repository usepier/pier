// The inspector tabs' shared shape: titled sections with room around
// them, and rows tall enough to read and hit, their hover reaching past
// the text.
import type { ReactNode } from "react";

/** A titled part of an inspector tab. */
export function Section({
  title,
  count,
  action,
  children,
}: {
  title: ReactNode;
  count?: number;
  /** A control at the title's end. */
  action?: ReactNode;
  children: ReactNode;
}) {
  return (
    <section className="border-b border-separator px-4 pt-3.5 pb-4 last:border-b-0">
      <header className="mb-2 flex min-h-6 items-center gap-1.5">
        <h3 className="text-xs font-semibold text-secondary">{title}</h3>
        {!!count && <span className="text-xs text-tertiary tabular-nums">{count}</span>}
        <span className="flex-1" />
        {action}
      </header>
      {children}
    </section>
  );
}

/** A row's look; add a hover for one that does something. */
export const rowClass =
  "-mx-2 flex min-h-8 w-[calc(100%+1rem)] items-center gap-2.5 rounded-[7px] px-2 py-1 text-left";

/** What a section says when it has nothing. */
export function Nothing({ children }: { children: ReactNode }) {
  return <div className="py-0.5 text-sm text-tertiary">{children}</div>;
}
