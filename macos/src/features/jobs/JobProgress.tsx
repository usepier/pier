import { CircleAlert, CircleCheck } from "lucide-react";
import { rpc } from "@/api/client";
import { useJob } from "@/state/jobs";
import { attempt } from "@/state/toasts";
import { Button } from "@/ui/Button";
import { Spinner } from "@/ui/Spinner";

function secs(ms: number) {
  const s = Math.round(ms / 1000);
  return s < 60 ? `${s}s` : `${Math.floor(s / 60)}m${String(s % 60).padStart(2, "0")}s`;
}

/** A background job's progress: its steps as they happen, then how it ended. */
export function JobProgress({ id, compact = false }: { id: string; compact?: boolean }) {
  const u = useJob(id);
  if (!u) {
    return (
      <div className="flex items-center gap-2 text-sm text-secondary">
        <Spinner /> starting…
      </div>
    );
  }
  const { job, events } = u;
  const steps = events.filter((e) => e.kind !== "done" && e.kind !== "failed");
  const shown = compact ? steps.slice(-3) : steps;
  return (
    <div className="flex flex-col gap-1.5">
      <ol className="flex flex-col gap-0.5 text-sm">
        {shown.map((e, i) => (
          <li
            key={i}
            className={`flex gap-2 ${e.kind === "warn" ? "text-warn" : e.kind === "note" ? "text-secondary" : "text-label"}`}
          >
            <span className="min-w-0 flex-1">{e.message}</span>
            <span className="shrink-0 tabular-nums text-tertiary">{secs(e.elapsedMs)}</span>
          </li>
        ))}
      </ol>
      <div className="flex items-center gap-2 text-sm">
        {job.state === "running" && (
          <>
            <Spinner />
            <span className="flex-1 text-secondary">working…</span>
            <Button size="sm" variant="plain" onPress={() => attempt(() => rpc.call("jobs.cancel", { id }))}>
              Stop
            </Button>
          </>
        )}
        {job.state === "done" && (
          <span className="flex items-center gap-1.5 text-ok">
            <CircleCheck size={14} /> {job.summary || "done"}
          </span>
        )}
        {(job.state === "failed" || job.state === "lost") && (
          <span className="selectable flex items-start gap-1.5 text-bad">
            <CircleAlert size={14} className="mt-0.5 shrink-0" /> {job.error}
          </span>
        )}
      </div>
    </div>
  );
}
