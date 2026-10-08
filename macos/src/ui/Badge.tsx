import type { ReactNode } from "react";

type Tone = "neutral" | "ok" | "warn" | "bad" | "accent" | "merged" | "info";
const tones: Record<Tone, string> = {
  neutral: "bg-selected text-secondary",
  ok: "bg-ok/15 text-ok",
  warn: "bg-warn/18 text-[color-mix(in_oklch,var(--color-warn)_75%,var(--label))]",
  bad: "bg-bad/15 text-bad",
  accent: "bg-accent-soft text-accent",
  merged: "bg-merged/15 text-merged",
  info: "bg-info/15 text-info",
};

export function Badge({
  tone = "neutral",
  children,
  title,
}: {
  tone?: Tone;
  children: ReactNode;
  title?: string;
}) {
  return (
    <span
      title={title}
      className={`inline-flex h-[18px] items-center gap-1 rounded-[5px] px-1.5 text-2xs font-semibold tracking-wide ${tones[tone]}`}
    >
      {children}
    </span>
  );
}
