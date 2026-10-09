import { useNavigate } from "@tanstack/react-router";
import { Anchor } from "lucide-react";
import type { Session } from "@/api/protocol.gen";
import { JobProgress } from "@/features/jobs/JobProgress";
import { ago, hourly, monthly } from "@/lib/format";
import { billed } from "@/lib/session";
import { needingAttention, reasonText, reasonTone, unseenChat, useSeen } from "@/state/attention";
import { useRunningJobs } from "@/state/jobs";
import { sessionTone, useOverview } from "@/state/overview";
import { useUI } from "@/state/ui";
import { Button } from "@/ui/Button";
import { Empty } from "@/ui/Empty";
import { Meter, Panel, Stat } from "@/ui/Panel";
import { StateDot } from "@/ui/StateDot";

const toneText: Record<string, string> = {
  warn: "text-warn",
  bad: "text-bad",
  ok: "text-ok",
  merged: "text-merged",
  accent: "text-accent",
};

const jobVerb: Record<string, string> = {
  create: "Creating",
  bake: "Baking",
  fill: "Filling the pool of",
  resize: "Resizing",
  deliver: "Waking, with a prompt,",
};

/** Everything at once: what runs, what it costs, what's in flight. */
export function HomePage() {
  const o = useOverview();
  const navigate = useNavigate();
  const openNew = useUI((s) => s.openNewSession);
  const jobs = useRunningJobs();
  const seen = useSeen((s) => s.seen);
  if (!o) return <Empty icon={<Anchor size={28} strokeWidth={1.5} />} title="Connecting to pier…" />;
  const live = o.sessions.filter((s) => !s.pooled && billed(s));
  const q = o.quota;
  const inbox = needingAttention(o.sessions, seen);
  return (
    <div className="h-full overflow-y-auto">
      <div className="mx-auto flex max-w-5xl flex-col gap-4 p-6">
        <header className="flex items-center gap-3">
          <h1 className="flex-1 text-xl font-semibold">Home</h1>
          <Button variant="primary" onPress={() => openNew()}>
            New Session
          </Button>
        </header>
        <div className="grid grid-cols-2 gap-4 rounded-card border border-separator bg-raised/60 p-4 md:grid-cols-5">
          <Stat label="Running" value={o.totals.running} />
          <Stat label="Parked" value={o.totals.parked} />
          <Stat label="Burning now" value={hourly(o.totals.hourlyUsd)} />
          <Stat
            label="Idle cost"
            value={monthly(o.totals.idleMonthlyUsd)}
            hint="parked disks, pools, images"
          />
          <div className="min-w-0">
            <div className="text-xs text-secondary">Capacity</div>
            {q && q.limit > 0 ? (
              <div className="mt-2 flex flex-col gap-1" title={q.detail}>
                <Meter value={q.used} max={q.limit} tone={q.used / q.limit > 0.85 ? "warn" : "accent"} />
                <div className="text-xs text-tertiary tabular-nums">
                  {q.used}/{q.limit} vCPUs
                </div>
              </div>
            ) : (
              <div className="text-lg text-tertiary">—</div>
            )}
          </div>
        </div>
        {inbox.length > 0 && (
          <Panel title={`Needs you · ${inbox.length}`}>
            <ul className="flex flex-col">
              {inbox.map(({ session: s, reason }) => (
                <li key={s.id}>
                  <button
                    type="button"
                    onClick={() => navigate({ to: "/session/$id", params: { id: s.id } })}
                    className="flex w-full items-center gap-2 rounded-ctl px-2 py-1.5 text-left text-sm hover:bg-hover"
                  >
                    <StateDot tone={sessionTone(s)} />
                    <span className="font-medium">{s.name}</span>
                    <span className={`shrink-0 text-xs ${toneText[reasonTone[reason]]}`}>
                      {reasonText[reason]}
                    </span>
                    <span className="min-w-0 flex-1 truncate text-xs text-secondary">
                      {reason === "finished"
                        ? unseenChat(s, seen)?.last
                        : reason === "needs_input"
                          ? s.chats?.find((c) => c.state === "waiting")?.ask
                          : reason === "create_failed" || reason === "setup_failed"
                            ? s.failReason
                            : s.pr && `#${s.pr.number} ${s.pr.title}`}
                    </span>
                    <span className="shrink-0 text-xs text-tertiary">{lastActive(s)}</span>
                  </button>
                </li>
              ))}
            </ul>
          </Panel>
        )}
        {jobs.length > 0 && (
          <Panel title="In progress">
            <div className="flex flex-col gap-4">
              {jobs.map((j) => (
                <div key={j.id}>
                  <div className="mb-1 text-sm font-medium">
                    {jobVerb[j.kind] ?? j.kind} {j.session || j.repo}
                  </div>
                  <JobProgress id={j.id} compact />
                </div>
              ))}
            </div>
          </Panel>
        )}
        <Panel title={`Running · ${live.length}`}>
          {live.length === 0 ? (
            <div className="py-4 text-center text-sm text-secondary">
              Nothing running — parked sessions cost disk only.
            </div>
          ) : (
            <ul className="flex flex-col">
              {live.map((s) => (
                <li key={s.id}>
                  <button
                    type="button"
                    onClick={() => navigate({ to: "/session/$id", params: { id: s.id } })}
                    className="flex w-full items-center gap-2 rounded-ctl px-2 py-1.5 text-left text-sm hover:bg-hover"
                  >
                    <StateDot tone={sessionTone(s)} />
                    <span className="font-medium">{s.name}</span>
                    <span className="text-secondary">{s.repo}</span>
                    <span className="flex-1 truncate text-right text-xs text-tertiary">
                      {s.activity || s.label}
                    </span>
                    <span className="w-20 text-right text-xs tabular-nums text-secondary">{s.costNote}</span>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </Panel>
      </div>
    </div>
  );
}

/** When the session's chats last changed state, as "5m ago". */
function lastActive(s: Session): string {
  const since = (s.chats ?? [])
    .map((c) => c.since)
    .sort()
    .at(-1);
  return since ? ago(since) : "";
}
