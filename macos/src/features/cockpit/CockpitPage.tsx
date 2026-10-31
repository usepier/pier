import { useQuery } from "@tanstack/react-query";
import { useNavigate, useParams } from "@tanstack/react-router";
import { FileText, FolderGit2, Hammer, Minus, Plus, Sparkles, SquarePlus } from "lucide-react";
import { type ReactNode, useState } from "react";
import { rpc } from "@/api/client";
import type { Repo, Session } from "@/api/protocol.gen";
import { RepoLinks, RepoTile } from "@/app/crumbs";
import { Toolbar } from "@/app/toolbar";
import { PRBadge } from "@/features/github/PRBadge";
import { linkCheckout } from "@/features/repos/link";
import { confirm } from "@/lib/confirm";
import { age, bytes, hourly, monthly, plural, until } from "@/lib/format";
import { billed, running } from "@/lib/session";
import { LOAD_WINDOW_MS, useLoad } from "@/state/history";
import { startJob, useRunningJobs } from "@/state/jobs";
import { sessionTone, useOverview, useRepo, useRepoSessions } from "@/state/overview";
import { poolState } from "@/state/pool";
import { promptFor } from "@/state/prompts";
import { useSetting } from "@/state/settings";
import { attempt, toast } from "@/state/toasts";
import { useUI } from "@/state/ui";
import { Badge } from "@/ui/Badge";
import { ActionButton, Button, IconButton } from "@/ui/Button";
import { Empty } from "@/ui/Empty";
import { LoadGraph } from "@/ui/LoadGraph";
import { LogView } from "@/ui/LogView";
import { Facts, Meter, Panel, Stat } from "@/ui/Panel";
import { Sheet } from "@/ui/Sheet";
import { Spinner } from "@/ui/Spinner";
import { StateDot } from "@/ui/StateDot";
import { HoverTip } from "@/ui/Tooltip";

const onboardPrompt =
  "Set this repository up for pier, using the pier-onboard skill: write .pier/setup.sh so a fresh session installs dependencies and starts services, check it runs, and open a pull request with it.";

export function CockpitPage() {
  const { name } = useParams({ from: "/repo/$name" });
  const repo = useRepo(name);
  const sessions = useRepoSessions(name);
  const openNew = useUI((s) => s.openNewSession);
  if (!repo) return <Empty title={name} detail="Loading…" />;
  const up = sessions.filter(billed).length;
  const parked = sessions.filter((s) => s.state === "parked").length;
  return (
    <div className="h-full overflow-y-auto">
      <Toolbar>
        <div data-tauri-drag-region className="flex min-w-0 flex-1 items-center gap-1">
          <div className="flex shrink-0 items-center gap-2.5 pr-2">
            <RepoTile name={repo.name} />
            <h1 className="text-md font-semibold">{repo.name}</h1>
          </div>
          <RepoLinks repo={repo} />
        </div>
        {!repo.checkout && (
          <Button icon={<FolderGit2 size={14} />} onPress={() => void linkCheckout(repo.name)}>
            Locate clone…
          </Button>
        )}
        <Button
          variant="primary"
          icon={<SquarePlus size={14} />}
          isDisabled={repo.needsImage}
          onPress={() => openNew(repo.name)}
        >
          New Session
        </Button>
      </Toolbar>
      <div className="mx-auto flex max-w-[1320px] flex-col gap-7 px-10 py-8">
        {repo.needsImage && <NeedsImage repo={repo} />}
        {repo.checkout && !repo.hasPier && (
          <Callout
            icon={<Sparkles size={18} className="text-accent" />}
            title={`${repo.name} isn't set up for pier yet`}
            detail="An agent can write its .pier/setup.sh, so sessions start with dependencies installed and services running."
            className="border-accent/30 bg-accent-soft"
          >
            <Button
              variant="primary"
              onPress={async () =>
                openNew(repo.name, {
                  branch: "pier-onboard",
                  prompt: await promptFor(repo.name, "onboard", onboardPrompt),
                })
              }
            >
              Set it up with an agent…
            </Button>
          </Callout>
        )}

        <div className="grid grid-cols-2 gap-8 rounded-card border border-separator bg-raised/60 px-6 py-5 md:grid-cols-5">
          <Stat label="Running" value={up} />
          <Stat label="Parked" value={parked} />
          <Stat label="Burning now" value={hourly(repo.hourlyUsd)} />
          <Stat label="Idle cost" value={monthly(repo.idleMonthlyUsd)} hint={idleParts(repo)} />
          <Stat
            label="Pool"
            value={`${repo.poolReady}/${repo.poolTarget}`}
            hint={repo.poolFilling ? `${repo.poolFilling} filling` : "ready"}
          />
        </div>

        <SessionsPanel sessions={sessions} />

        <PullRequestsPanel sessions={sessions.filter((s) => s.pr)} />

        {sessions.length > 0 && <ResourcesPanel sessions={sessions.filter(running)} />}

        <div className="grid gap-6 lg:grid-cols-3">
          <PoolPanel repo={repo} />
          <ImagePanel repo={repo} />
          <CapacityPanel />
        </div>
      </div>
    </div>
  );
}

/** What a repo's idle cost pays for, in a few words. */
function idleParts(r: Repo): string {
  const parked = Math.max(0, r.idleMonthlyUsd - r.monthlyUsd);
  const pooled = Math.max(0, r.monthlyUsd - r.imageMonthlyUsd);
  const parts = [
    r.imageMonthlyUsd > 0 && "the session image",
    pooled > 0 && "the pool's disks",
    parked > 0 && "parked sessions' disks",
  ].filter(Boolean) as string[];
  if (parts.length === 0) return "nothing stored";
  return parts.length === 1 ? parts[0]! : `${parts.slice(0, -1).join(", ")} and ${parts.at(-1)}`;
}

/** A notice across the page: what it is, why, and the one thing to do. */
function Callout({
  icon,
  title,
  detail,
  className,
  children,
}: {
  icon: ReactNode;
  title: string;
  detail: ReactNode;
  className: string;
  children?: ReactNode;
}) {
  return (
    <div className={`flex items-start gap-4 rounded-card border px-6 py-5 ${className}`}>
      <div className="pt-0.5">{icon}</div>
      <div className="min-w-0 flex-1">
        <div className="text-md font-semibold">{title}</div>
        <div className="mt-1.5 text-sm leading-relaxed text-secondary">{detail}</div>
      </div>
      {children && <div className="shrink-0 self-center">{children}</div>}
    </div>
  );
}

const bake = async (repo: Repo) => {
  const path = repo.checkout?.path;
  if (path) await startJob(() => rpc.call("image.bake", { path }));
};

/** A repo whose sessions can't start until it has its image: say so, and offer the bake. */
function NeedsImage({ repo }: { repo: Repo }) {
  return (
    <Callout
      icon={<Hammer size={18} className="text-warn" />}
      title={repo.baking ? `Baking ${repo.name}'s session image` : `${repo.name} needs its session image`}
      detail={
        repo.baking
          ? "Sessions and the pool can start once it's done."
          : "Its .pier/bake.sh installs toolchains its sessions need, so new sessions and the pool wait for a bake. It runs in the background on a temporary machine, about ten minutes."
      }
      className="border-warn/30 bg-warn/8"
    >
      {!repo.baking && (
        <ActionButton
          variant="primary"
          icon={<Hammer size={13} />}
          isDisabled={!repo.checkout}
          action={() => bake(repo)}
        >
          Bake now
        </ActionButton>
      )}
    </Callout>
  );
}

function PullRequestsPanel({ sessions }: { sessions: Session[] }) {
  const navigate = useNavigate();
  if (sessions.length === 0) return null;
  const order: Record<string, number> = { open: 0, draft: 1, merged: 2, closed: 3 };
  const list = [...sessions].sort(
    (a, b) => order[a.pr!.state]! - order[b.pr!.state]! || b.pr!.number - a.pr!.number,
  );
  return (
    <Panel title={`Pull requests · ${list.length}`}>
      <ul className="-mx-2 flex flex-col">
        {list.map((s) => {
          const p = s.pr!;
          return (
            <li key={s.id}>
              <button
                type="button"
                onClick={() => navigate({ to: "/session/$id", params: { id: s.id } })}
                className="flex h-10 w-full items-center gap-3 rounded-ctl px-2 text-left text-sm hover:bg-hover"
              >
                <PRBadge pr={p} />
                <span className="min-w-0 flex-1 truncate">{p.title}</span>
                <span className="shrink-0 text-secondary">{s.name}</span>
                <span className="w-36 shrink-0 text-right text-secondary">
                  {p.state === "open" || p.state === "draft"
                    ? p.checks === "failing"
                      ? "checks failing"
                      : p.review === "changes_requested"
                        ? "changes requested"
                        : p.review === "approved"
                          ? "approved"
                          : p.checks === "pending"
                            ? "checks running"
                            : "in review"
                    : p.state}
                </span>
              </button>
            </li>
          );
        })}
      </ul>
    </Panel>
  );
}

function SessionsPanel({ sessions }: { sessions: Session[] }) {
  const navigate = useNavigate();
  return (
    <Panel title={`Sessions · ${sessions.length}`}>
      {sessions.length === 0 ? (
        <div className="py-6 text-center text-secondary">No sessions yet.</div>
      ) : (
        <table className="w-full text-sm">
          <thead>
            <tr className="text-left text-xs text-secondary">
              <th className="pb-2 pr-4 font-medium">Name</th>
              <th className="pb-2 pr-4 font-medium">State</th>
              <th className="pb-2 pr-4 font-medium">Machine</th>
              <th className="pb-2 pr-4 font-medium">Cost</th>
              <th className="pb-2 font-medium">Age</th>
            </tr>
          </thead>
          <tbody>
            {sessions.map((s) => (
              <tr
                key={s.id}
                onClick={() => navigate({ to: "/session/$id", params: { id: s.id } })}
                className="h-11 cursor-default border-t border-separator hover:bg-hover"
              >
                <td className="pr-4">
                  <span className="flex items-center gap-2.5">
                    <StateDot tone={sessionTone(s)} />
                    <span className="truncate font-medium">{s.name}</span>
                  </span>
                </td>
                <td className="pr-4 text-secondary">
                  <span className="flex items-center gap-2">
                    {s.label}
                    {s.setup === "running" && <Badge tone="info">setup</Badge>}
                    {s.setup === "failed" && <Badge tone="bad">setup failed</Badge>}
                    {s.strained && <Badge tone="warn">strained</Badge>}
                  </span>
                </td>
                <td className="pr-4 text-secondary">{s.machineType}</td>
                <td className="pr-4 tabular-nums text-secondary">{s.costNote}</td>
                <td className="tabular-nums text-secondary">{age(s.createdAt)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </Panel>
  );
}

function ResourceRow({ session: s }: { session: Session }) {
  const navigate = useNavigate();
  const { readings, now: r } = useLoad(s);
  const park = until(s.parkAt);
  return (
    <button
      type="button"
      onClick={() => navigate({ to: "/session/$id", params: { id: s.id } })}
      className="grid h-14 w-full grid-cols-[minmax(0,1.2fr)_minmax(0,2fr)_minmax(0,2fr)_8rem] items-center gap-6 border-t border-separator px-2 text-left text-sm hover:bg-hover"
    >
      <span className="flex min-w-0 items-center gap-2.5">
        <StateDot tone={sessionTone(s)} />
        <span className="truncate font-medium">{s.name}</span>
      </span>
      <span className="flex min-w-0 items-center gap-3">
        <LoadGraph
          label={`${s.name}'s CPU over the last 10 minutes`}
          points={readings.map((x) => ({ t: x.t, v: x.cpu }))}
          window={LOAD_WINDOW_MS}
          min={10}
          max={100}
        />
        <span className="w-10 shrink-0 text-right tabular-nums">{r ? `${r.cpu.toFixed(0)}%` : "—"}</span>
      </span>
      <span className="flex min-w-0 items-center gap-3">
        <LoadGraph
          label={`${s.name}'s memory over the last 10 minutes`}
          points={readings.map((x) => ({ t: x.t, v: x.mem }))}
          window={LOAD_WINDOW_MS}
          min={(r?.memTotal ?? 0) / 4}
          max={r?.memTotal ?? 1}
          className="text-info"
        />
        <span className="shrink-0 whitespace-nowrap text-right tabular-nums text-secondary">
          {r ? `${bytes(r.memUsed)} / ${bytes(r.memTotal)}` : "—"}
        </span>
      </span>
      <span className="text-right text-secondary">
        {s.strained ? (
          <Badge tone="warn">strained</Badge>
        ) : park ? (
          `parks in ${park}`
        ) : s.activity ? (
          "busy"
        ) : (
          ""
        )}
      </span>
    </button>
  );
}

function ResourcesPanel({ sessions }: { sessions: Session[] }) {
  if (sessions.length === 0) {
    return (
      <Panel title="Load">
        <div className="py-4 text-center text-secondary">
          Nothing's running. CPU and memory show here while a session runs.
        </div>
      </Panel>
    );
  }
  return (
    <Panel title="Load · last 10 minutes">
      <div className="-mx-2">
        <div className="grid grid-cols-[minmax(0,1.2fr)_minmax(0,2fr)_minmax(0,2fr)_8rem] gap-6 px-2 pb-2 text-xs font-medium text-secondary">
          <span>Session</span>
          <span>CPU</span>
          <span>Memory</span>
          <span />
        </div>
        {sessions.map((s) => (
          <ResourceRow key={s.id} session={s} />
        ))}
      </div>
    </Panel>
  );
}

function PoolPanel({ repo }: { repo: Repo }) {
  const path = repo.checkout?.path;
  const setSize = (n: number) =>
    path
      ? attempt(async () => {
          await rpc.call("pool.setSize", { path, size: Math.max(0, n) });
        })
      : toast(`Locate your clone of ${repo.name} to change its pool`, "info");
  const fill = () => path && startJob(() => rpc.call("pool.fill", { path }));
  const drain = async () => {
    if (
      !(await confirm(
        `Drain ${repo.name}'s pool?`,
        "Every pooled session of this repo is destroyed. New sessions then start without a head start until the pool refills.",
        { ok: "Drain", destructive: true },
      ))
    )
      return;
    const r = await attempt(() => rpc.call("pool.drain", { repo: repo.name }));
    if (r) toast(`Removed ${plural(r.removed, "pooled session")}`, "ok");
  };
  const fills = useRunningJobs((j) => j.kind === "fill" && j.repo === repo.name);
  const state = poolState(repo, useSetting("speed.auto_refill") === "true", fills.length > 0);
  return (
    <Panel
      title="Pool"
      actions={
        <div className="flex items-center gap-1">
          <IconButton
            label="Keep one fewer"
            className="size-7"
            isDisabled={repo.poolTarget === 0}
            onPress={() => setSize(repo.poolTarget - 1)}
          >
            <Minus size={14} />
          </IconButton>
          <span className="w-6 text-center tabular-nums">{repo.poolTarget}</span>
          <IconButton
            label="Keep one more"
            className="size-7"
            isDisabled={repo.poolTarget >= 8}
            onPress={() => setSize(repo.poolTarget + 1)}
          >
            <Plus size={14} />
          </IconButton>
        </div>
      }
    >
      <div className="flex flex-col gap-4">
        <div
          className={`rounded-ctl px-3.5 py-3 ${state.kind === "failed" ? "bg-bad/10" : state.kind === "short" ? "bg-warn/12" : state.kind === "ready" ? "bg-ok/10" : "bg-raised text-secondary"}`}
        >
          {state.detail}
          {state.kind === "failed" && repo.poolError && (
            <div className="mt-2.5">
              <PoolLog repo={repo.name} />
            </div>
          )}
        </div>
        <Facts
          rows={[
            ["Ready", `${repo.poolReady} of ${repo.poolTarget}`],
            ["Costs", monthly(repo.monthlyUsd)],
          ]}
        />
        <div className="flex gap-2 pt-1">
          <ActionButton
            size="sm"
            isDisabled={!path || repo.poolTarget === 0 || repo.needsImage || fills.length > 0}
            action={async () => {
              await fill();
            }}
          >
            {state.kind === "failed" ? "Fill again" : "Fill now"}
          </ActionButton>
          <ActionButton
            size="sm"
            variant="plain"
            isDisabled={repo.poolReady + repo.poolFilling === 0}
            action={drain}
          >
            Drain
          </ActionButton>
        </div>
      </div>
    </Panel>
  );
}

/** The pool's log, in a sheet: a fill's whole story, when its one line isn't enough. */
function PoolLog({ repo }: { repo: string }) {
  const [open, setOpen] = useState(false);
  const log = useQuery({
    queryKey: ["pool-log", repo],
    queryFn: () => rpc.call("pool.log", { repo, lines: 400 }),
    enabled: open,
  });
  return (
    <>
      <Button size="sm" icon={<FileText size={12} />} onPress={() => setOpen(true)}>
        Show log
      </Button>
      <Sheet open={open} onOpenChange={setOpen} title={`${repo}'s pool log`} width={760}>
        {log.data ? (
          <LogView text={log.data.text} className="max-h-[60vh]" empty="nothing logged" />
        ) : log.error ? (
          <div className="text-sm text-bad">{(log.error as Error).message}</div>
        ) : (
          <Spinner />
        )}
      </Sheet>
    </>
  );
}

const imageKinds: Record<string, string> = {
  prebuilt: "Prebuilt",
  toolchain: "Toolchains only",
  unknown: "Baked",
  "": "None yet",
};

function ImagePanel({ repo }: { repo: Repo }) {
  const path = repo.checkout?.path;
  return (
    <Panel
      title="Session image"
      actions={
        <ActionButton
          size="sm"
          icon={<Hammer size={12} />}
          isDisabled={!path || repo.baking}
          action={() => bake(repo)}
        >
          {repo.baking ? "Baking…" : "Bake"}
        </ActionButton>
      }
    >
      <div className="flex flex-col gap-4">
        <Facts
          rows={[
            ["Image", imageKinds[repo.imageKind] ?? repo.imageKind],
            ["Baked", repo.bakedAt ? `${age(repo.bakedAt)} ago` : "—"],
          ]}
        />
        {/* What's out of date, in one line; the banner covers a repo needing its image. */}
        {!repo.needsImage && !repo.baking && repo.reminders.length > 0 && (
          <HoverTip content={repo.reminders.map((r) => r.message).join(" ")}>
            <span className="rounded-ctl bg-warn/10 px-3.5 py-2.5">Out of date — rebake</span>
          </HoverTip>
        )}
        {!path && <p className="text-secondary">Bakes run from your local clone — locate it first.</p>}
      </div>
    </Panel>
  );
}

function CapacityPanel() {
  const q = useOverview()?.quota;
  return (
    <Panel title="Capacity">
      {q && q.limit > 0 ? (
        <div className="flex flex-col gap-3">
          <Meter value={q.used} max={q.limit} tone={q.used / q.limit > 0.85 ? "warn" : "accent"} />
          <div>{q.detail}</div>
        </div>
      ) : (
        <div className="text-secondary">Quota unknown — the account may not allow reading it.</div>
      )}
    </Panel>
  );
}
