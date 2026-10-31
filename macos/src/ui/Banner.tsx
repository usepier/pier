import type { ReactNode } from "react";

type Tone = "info" | "warn" | "bad";
const tones: Record<Tone, string> = {
  info: "bg-info/12 text-label",
  warn: "bg-warn/15 text-label",
  bad: "bg-bad/12 text-label",
};

/** A full-width strip under the toolbar for app-wide conditions. */
export function Banner({
  tone = "info",
  children,
  actions,
}: {
  tone?: Tone;
  children: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <div className={`flex items-center gap-3 border-b border-separator px-4 py-1.5 text-sm ${tones[tone]}`}>
      <div className="min-w-0 flex-1">{children}</div>
      {actions && <div className="flex shrink-0 gap-2">{actions}</div>}
    </div>
  );
}
