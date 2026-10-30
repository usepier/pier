import { Check, ChevronRight, CircleAlert, CircleCheck, Info, TriangleAlert } from "lucide-react";
import { useState } from "react";
import { rpc } from "@/api/client";
import type { Event, Job } from "@/api/protocol.gen";
import { useNow } from "@/lib/useNow";
import { useJob } from "@/state/jobs";
import { attempt } from "@/state/toasts";
import { ActionButton } from "@/ui/Button";
import { Meter } from "@/ui/Panel";
import { Reveal } from "@/ui/Reveal";
import { Spinner } from "@/ui/Spinner";

function secs(ms: number) {
  const s = Math.max(0, Math.round(ms / 1000));
  return s < 60 ? `${s}s` : `${Math.floor(s / 60)}m ${String(s % 60).padStart(2, "0")}s`;
}

/**
 * The stages a kind of job goes through, each known by its steps' words: a
 * job reads as where it is of a few, not as its log.
 */
const stagesOf: Partial<Record<Job["kind"], { label: string; match: RegExp }[]>> = {
  bake: [
    { label: "Machine", match: /bake instance|baking from/i },
    { label: "Toolchains", match: /running \.pier\/bake\.sh/i },
    { label: "Repo setup", match: /pushing secrets|checking out|\.pier\/setup\.sh|setup done/i },
    { label: "Image", match: /scrubbing|imaging|saving image|snapshot/i },
  ],
  create: [
    { label: "Machine", match: /launched|claimed|waiting for ssh|resuming/i },
    { label: "Repo", match: /pushing|bootstrapping|switching to your branch/i },
    { label: "Setup", match: /waiting for \.pier\/setup\.sh/i },
  ],
  fill: [
    { label: "Machine", match: /launched|waiting for ssh/i },
    { label: "Repo", match: /pushing|bootstrapping/i },
    { label: "Setup", match: /waiting for \.pier\/setup\.sh/i },
    { label: "Parked", match: /parking/i },
  ],
};

/** A step's words, as a sentence. */
const sentence = (m: string) => m.charAt(0).toUpperCase() + m.slice(1);

/**
 * A background job's progress: the stages it goes through, the step it's
 * on (with how long it has run), and every step on request; then how it
 * ended.
 */
export function JobProgress({ id }: { id: string; compact?: boolean }) {
  const u = useJob(id);
  const [open, setOpen] = useState(false);
  const now = useNow(1000);
  if (!u) {
    return (
      <div className="flex items-center gap-2 text-sm text-secondary">
        <Spinner /> Starting…
      </div>
    );
  }
  const { job, events } = u;
  const steps = events.filter((e) => e.kind !== "done" && e.kind !== "failed");
  const running = job.state === "running";
  const failed = job.state === "failed" || job.state === "lost";
  const stages = stagesOf[job.kind];
  let at = -1;
  if (stages)
    for (const e of steps) {
      const i = stages.findIndex((s) => s.match.test(e.message));
      if (i > at) at = i;
    }
  const latest = [...steps].reverse().find((e) => e.kind === "step");
  const warnings = steps.filter((e) => e.kind === "warn");
  const pct = latest?.message.match(/(\d+)%/)?.[1];
  const ran = (job.endedAt ? Date.parse(job.endedAt) : now) - Date.parse(job.startedAt);

  return (
    <div className="flex flex-col gap-3">
      {stages && (
        <ol className="flex items-center gap-2" aria-label="Stages">
          {stages.map((s, i) => {
            const done = i < at || (i === at && job.state === "done") || (job.state === "done" && at < 0);
            const here = i === at && !done;
            return (
              <li key={s.label} className="flex min-w-0 flex-1 items-center gap-2 last:flex-none">
                <span
                  className={`flex shrink-0 items-center gap-1.5 text-sm ${here ? "font-medium text-label" : done ? "text-secondary" : "text-tertiary"}`}
                  aria-current={here ? "step" : undefined}
                >
                  {done ? (
                    <span className="flex size-4 items-center justify-center rounded-full bg-ok/15 text-ok">
                      <Check size={10} strokeWidth={3} />
                    </span>
                  ) : here && failed ? (
                    <CircleAlert size={16} className="text-bad" />
                  ) : here ? (
                    <Spinner size={14} className="text-accent" />
                  ) : (
                    <span className="size-4 rounded-full border border-strong" />
                  )}
                  {s.label}
                </span>
                {i < stages.length - 1 && (
                  <span className={`h-px min-w-3 flex-1 ${i < at ? "bg-ok/40" : "bg-separator"}`} />
                )}
              </li>
            );
          })}
        </ol>
      )}

      {running && (
        <div className="flex items-baseline gap-3">
          <span className="min-w-0 flex-1 truncate text-base" title={latest?.message}>
            {latest ? sentence(latest.message) : "Starting…"}
          </span>
          <span className="shrink-0 text-sm text-tertiary tabular-nums">{secs(ran)}</span>
        </div>
      )}
      {running && pct && (
        <div className="flex items-center gap-3">
          <Meter value={Number(pct)} max={100} />
          <span className="w-10 shrink-0 text-right text-sm text-secondary tabular-nums">{pct}%</span>
        </div>
      )}
      {job.state === "done" && (
        <div className="flex items-center gap-2 text-base text-ok">
          <CircleCheck size={15} /> {job.summary ? sentence(job.summary) : "Done"}
          <span className="text-sm text-tertiary tabular-nums">in {secs(ran)}</span>
        </div>
      )}
      {failed && (
        <div className="selectable flex items-start gap-2 text-base text-bad">
          <CircleAlert size={15} className="mt-0.5 shrink-0" /> {job.error || "It stopped."}
        </div>
      )}
      {warnings.map((w, i) => (
        <div key={i} className="flex items-start gap-2 text-sm text-warn">
          <TriangleAlert size={13} className="mt-0.5 shrink-0" /> {sentence(w.message)}
        </div>
      ))}

      <div className="flex items-center gap-2">
        {steps.length > 0 && (
          <button
            type="button"
            aria-expanded={open}
            onClick={() => setOpen(!open)}
            className="-ml-1.5 flex items-center gap-1 rounded-[6px] px-1.5 py-0.5 text-sm text-secondary transition-colors hover:bg-hover hover:text-label"
          >
            <ChevronRight
              size={12}
              className={`transition-transform duration-200 ${open ? "rotate-90" : ""}`}
            />
            {open ? "Hide the steps" : `Every step (${steps.length})`}
          </button>
        )}
        <span className="flex-1" />
        {running && (
          <ActionButton
            size="sm"
            variant="plain"
            action={() => attempt(() => rpc.call("jobs.cancel", { id }))}
          >
            Stop
          </ActionButton>
        )}
      </div>
      <Reveal open={open}>
        <ol className="flex flex-col gap-1 border-l border-separator pl-3 text-sm">
          {steps.map((e, i) => (
            <StepRow key={i} e={e} last={i === steps.length - 1 && running} />
          ))}
        </ol>
      </Reveal>
    </div>
  );
}

function StepRow({ e, last }: { e: Event; last: boolean }) {
  const icon =
    e.kind === "warn" ? (
      <TriangleAlert size={12} className="text-warn" />
    ) : e.kind === "note" ? (
      <Info size={12} className="text-tertiary" />
    ) : last ? (
      <Spinner size={11} />
    ) : (
      <Check size={12} className="text-ok" />
    );
  return (
    <li className={`flex items-start gap-2 ${e.kind === "note" ? "text-secondary" : "text-label"}`}>
      <span className="mt-[3px] shrink-0">{icon}</span>
      <span className="min-w-0 flex-1 break-words">{sentence(e.message)}</span>
      {e.elapsedMs > 0 && <span className="shrink-0 text-tertiary tabular-nums">+{secs(e.elapsedMs)}</span>}
    </li>
  );
}
