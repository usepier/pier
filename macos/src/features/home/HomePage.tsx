import { useNavigate } from "@tanstack/react-router";
import { Anchor, SquarePlus } from "lucide-react";
import type { Session } from "@/api/protocol.gen";
import { Toolbar } from "@/app/toolbar";
import { JobProgress } from "@/features/jobs/JobProgress";
import { ago, hourly, monthly } from "@/lib/format";
import { billed } from "@/lib/session";
import { needingAttention, reasonText, reasonTone, unseenChat, useSeen } from "@/state/attention";
import { useRunningJobs } from "@/state/jobs";
import { sessionTone, useOverview } from "@/state/overview";
import { useUI } from "@/state/ui";
import { Button } from "@/ui/Button";
import { Empty } from "@/ui/Empty";
import { Meter, Panel, StatCard } from "@/ui/Panel";
import { Spinner } from "@/ui/Spinner";
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
  // A bake this app didn't start (a terminal's \`pier bake\`) is in progress
  // too: the repos say so even without its job.
  const bakingElsewhere = o.repos.filter(
    (r) => r.baking && !jobs.some((j) => j.kind === "bake" && j.repo === r.name),
  );
  return (
    <div className="flex h-full flex-col">
      <Toolbar>
        <h1 data-tauri-drag-region className="flex-1 text-base font-semibold">
          Home
        </h1>
        <Button variant="primary" icon={<SquarePlus size={14} />} onPress={() => openNew()}>
          New Session
        </Button>
      </Toolbar>
      {/* Centered in the window while it's short; it scrolls once it isn't. */}
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto flex min-h-full max-w-5xl flex-col justify-center gap-4 px-6 pt-6 pb-[8vh]">
          <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-5">
            <StatCard label="Running" value={o.totals.running} hint="sessions" />
            <StatCard label="Parked" value={o.totals.parked} hint="disk only" />
            <StatCard label="Burning now" value={hourly(o.totals.hourlyUsd)} hint="running machines" />
            <StatCard
              label="Idle cost"
              value={monthly(o.totals.idleMonthlyUsd)}
              hint="parked disks, pools, images"
            />
            {q && q.limit > 0 ? (
              <StatCard
                label="Capacity"
                value={`${q.used} / ${q.limit}`}
                hint="vCPUs in use"
                title={q.detail}
              >
                <Meter value={q.used} max={q.limit} tone={q.used / q.limit > 0.85 ? "warn" : "accent"} />
              </StatCard>
            ) : (
              <StatCard label="Capacity" value="—" hint="vCPUs in use" />
            )}
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
          {(jobs.length > 0 || bakingElsewhere.length > 0) && (
            <Panel title="In progress">
              <div className="flex flex-col gap-3">
                {bakingElsewhere.map((r) => (
                  <section
                    key={r.name}
                    aria-label={`Baking ${r.name}`}
                    className="flex items-center gap-3 rounded-card border border-separator px-4 py-3.5"
                  >
                    <Spinner size={14} className="text-accent" />
                    <div className="min-w-0 flex-1">
                      <div className="text-base font-medium">
                        Baking {r.name}
                        <span className="font-normal text-secondary">’s session image</span>
                      </div>
                      <div className="text-sm text-secondary">
                        Started outside the app — new sessions and the pool wait for it.
                      </div>
                    </div>
                    <Button
                      size="sm"
                      onPress={() => navigate({ to: "/repo/$name", params: { name: r.name } })}
                    >
                      Open {r.name}
                    </Button>
                  </section>
                ))}
                {jobs.map((j) => (
                  <section
                    key={j.id}
                    aria-label={`${jobVerb[j.kind] ?? j.kind} ${j.session || j.repo}`}
                    className="rounded-card border border-separator px-4 pt-3.5 pb-3"
                  >
                    <h3 className="mb-3 text-base font-medium">
                      {jobVerb[j.kind] ?? j.kind} <span className="text-label">{j.session || j.repo}</span>
                      {j.kind === "bake" && (
                        <span className="font-normal text-secondary">’s session image</span>
                      )}
                    </h3>
                    <JobProgress id={j.id} />
                  </section>
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
                      <span className="w-20 text-right text-xs tabular-nums text-secondary">
                        {s.costNote}
                      </span>
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </Panel>
        </div>
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
