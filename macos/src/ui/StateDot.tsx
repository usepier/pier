import type { Tone } from "@/state/overview";

const tones: Record<Tone, string> = {
  ok: "bg-ok",
  busy: "bg-accent animate-pulse-soft",
  idle: "bg-ok/55",
  parked: "border border-tertiary bg-transparent",
  starting: "border-[1.5px] border-secondary border-t-transparent animate-spin-slow",
  bad: "bg-bad",
  gone: "bg-quaternary",
};

/** The colored dot that says, at a glance, where a session is. */
export function StateDot({ tone, className = "" }: { tone: Tone; className?: string }) {
  return (
    <span
      aria-hidden="true"
      className={`inline-block size-2 shrink-0 rounded-full ${tones[tone]} ${className}`}
    />
  );
}
