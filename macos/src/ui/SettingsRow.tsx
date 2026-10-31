import type { ReactNode } from "react";

/** One setting: what it is and what it does on the left, its control on
 * the right. */
export function SettingsRow({
  title,
  description,
  hint,
  control,
  children,
  id,
}: {
  title: ReactNode;
  description?: ReactNode;
  /** A smaller line under the description: a shortcut, a default. */
  hint?: ReactNode;
  control?: ReactNode;
  /** Below the row, full width: a log, a list. */
  children?: ReactNode;
  id?: string;
}) {
  return (
    <div id={id} className="border-b border-separator py-4 last:border-b-0">
      <div className="flex items-start gap-6">
        <div className="min-w-0 flex-1">
          <div className="text-base font-medium text-label">{title}</div>
          {description && <div className="mt-1 text-base text-secondary">{description}</div>}
          {hint && <div className="mt-1 text-sm text-tertiary">{hint}</div>}
        </div>
        {control && <div className="flex shrink-0 items-center gap-2 pt-0.5">{control}</div>}
      </div>
      {children && <div className="mt-3">{children}</div>}
    </div>
  );
}

/** A titled run of settings rows. */
export function SettingsSection({
  title,
  description,
  children,
}: {
  title: ReactNode;
  description?: ReactNode;
  children: ReactNode;
}) {
  return (
    <section className="mb-10">
      <h2 className="font-display text-lg font-semibold text-label">{title}</h2>
      {description && <p className="mt-1 text-base text-secondary">{description}</p>}
      <div className="mt-3">{children}</div>
    </section>
  );
}
