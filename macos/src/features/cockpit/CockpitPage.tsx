import { useNavigate, useParams } from "@tanstack/react-router";
import { FolderGit2, Hammer, Minus, Plus, SquarePlus } from "lucide-react";
import { rpc } from "@/api/client";
import type { Repo, Session } from "@/api/protocol.gen";
import { JobProgress } from "@/features/jobs/JobProgress";
import { linkCheckout } from "@/features/repos/link";
import { confirm } from "@/lib/confirm";
import { age, hourly, monthly, plural } from "@/lib/format";
import { trackJob, useRunningJobs } from "@/state/jobs";
import { sessionTone, useOverview, useRepo, useRepoSessions } from "@/state/overview";
import { attempt, toast } from "@/state/toasts";
import { useUI } from "@/state/ui";
import { Badge } from "@/ui/Badge";
import { Button, IconButton } from "@/ui/Button";
import { Empty } from "@/ui/Empty";
import { Facts, Meter, Panel, Stat } from "@/ui/Panel";
import { StateDot } from "@/ui/StateDot";

export function CockpitPage() {
  const { name } = useParams({ from: "/repo/$name" });
  const repo = useRepo(name);
  const sessions = useRepoSessions(name);
  const openNew = useUI((s) => s.openNewSession);
  if (!repo) return <Empty title={name} detail="Loading…" />;
  const running = sessions.filter((s) => ["creating", "running", "working", "idle"].includes(s.state)).length;
  const parked = sessions.filter((s) => s.state === "parked").length;
  return (
    <div className="h-full overflow-y-auto">
      <div className="mx-auto flex max-w-6xl flex-col gap-4 p-6">
        <header className="flex items-start gap-4">
          <div className="min-w-0 flex-1">
            <h1 className="truncate text-xl font-semibold">{repo.name}</h1>
            <div className="selectable mt-0.5 truncate text-sm text-secondary">
              {repo.checkout ? repo.checkout.path : "No local clone linked"}
              {repo.origin && <span className="text-tertiary"> · {repo.origin}</span>}
            </div>
          </div>
          {!repo.checkout && (
            <Button icon={<FolderGit2 size={14} />} onPress={() => void linkCheckout(repo.name)}>
              Locate clone…
            </Button>
          )}
          <Button variant="primary" icon={<SquarePlus size={14} />} onPress={() => openNew(repo.name)}>
            New Session
          </Button>
        </header>

        <div className="grid grid-cols-2 gap-4 rounded-card border border-separator bg-raised/60 p-4 md:grid-cols-5">
          <Stat label="Running" value={running} />
          <Stat label="Parked" value={parked} />
          <Stat label="Burning now" value={hourly(repo.hourlyUsd)} />
          <Stat label="Idle cost" value={monthly(repo.idleMonthlyUsd)} hint="disks, pool and image" />
          <Stat
            label="Pool"
            value={`${repo.poolReady}/${repo.poolTarget}`}
            hint={repo.poolFilling ? `${repo.poolFilling} filling` : "ready"}
          />
        </div>

        <SessionsPanel sessions={sessions} />

        <div className="grid gap-4 lg:grid-cols-3">
          <PoolPanel repo={repo} />
          <ImagePanel repo={repo} />
          <CapacityPanel />
        </div>
      </div>
    </div>
  );
}

function SessionsPanel({ sessions }: { sessions: Session[] }) {
  const navigate = useNavigate();
  return (
    <Panel title={`Sessions · ${sessions.length}`}>
      {sessions.length === 0 ? (
        <div className="py-4 text-center text-sm text-secondary">No sessions yet.</div>
      ) : (
        <table className="w-full text-sm">
          <thead>
            <tr className="text-left text-xs text-secondary">
              <th className="py-1 pr-3 font-medium">Name</th>
              <th className="py-1 pr-3 font-medium">State</th>
              <th className="py-1 pr-3 font-medium">Machine</th>
              <th className="py-1 pr-3 font-medium">Cost</th>
              <th className="py-1 font-medium">Age</th>
            </tr>
          </thead>
          <tbody>
            {sessions.map((s) => (
              <tr
                key={s.id}
                onClick={() => navigate({ to: "/session/$id", params: { id: s.id } })}
                className="cursor-default border-t border-separator hover:bg-hover"
              >
                <td className="py-1.5 pr-3">
                  <span className="flex items-center gap-2">
                    <StateDot tone={sessionTone(s)} />
                    <span className="truncate font-medium">{s.name}</span>
                  </span>
                </td>
                <td className="py-1.5 pr-3 text-secondary">
                  <span className="flex items-center gap-1.5">
                    {s.label}
                    {s.setup === "running" && <Badge tone="info">setup</Badge>}
                    {s.setup === "failed" && <Badge tone="bad">setup failed</Badge>}
                    {s.strained && <Badge tone="warn">strained</Badge>}
                  </span>
                </td>
                <td className="py-1.5 pr-3 text-secondary">{s.machineType}</td>
                <td className="py-1.5 pr-3 tabular-nums text-secondary">{s.costNote}</td>
                <td className="py-1.5 tabular-nums text-secondary">{age(s.createdAt)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
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
  const fill = async () => {
    if (!path) return;
    const j = await attempt(() => rpc.call("pool.fill", { path }));
    if (j) trackJob(j);
  };
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
  return (
    <Panel
      title="Pool"
      actions={
        <div className="flex items-center gap-1">
          <IconButton
            label="Keep one fewer"
            className="size-6"
            isDisabled={repo.poolTarget === 0}
            onPress={() => setSize(repo.poolTarget - 1)}
          >
            <Minus size={13} />
          </IconButton>
          <span className="w-5 text-center text-sm tabular-nums">{repo.poolTarget}</span>
          <IconButton
            label="Keep one more"
            className="size-6"
            isDisabled={repo.poolTarget >= 8}
            onPress={() => setSize(repo.poolTarget + 1)}
          >
            <Plus size={13} />
          </IconButton>
        </div>
      }
    >
      <p className="mb-2 text-xs text-secondary">
        Parked, set-up sessions a new session claims instead of creating — disk-only cost while they wait.
      </p>
      <Facts
        rows={[
          ["Ready", `${repo.poolReady} of ${repo.poolTarget}`],
          ["Filling", repo.poolFilling ? String(repo.poolFilling) : "—"],
          ["Recycling", repo.poolStale ? String(repo.poolStale) : "—"],
          ["Ages", repo.poolAges.length ? repo.poolAges.join(", ") : "—"],
          ["Costs", monthly(repo.monthlyUsd)],
        ]}
      />
      {fills[0] && (
        <div className="mt-3">
          <JobProgress id={fills[0].id} compact />
        </div>
      )}
      <div className="mt-3 flex gap-2">
        <Button size="sm" isDisabled={!path || repo.poolTarget === 0} onPress={() => void fill()}>
          Fill now
        </Button>
        <Button
          size="sm"
          variant="plain"
          isDisabled={repo.poolReady + repo.poolFilling === 0}
          onPress={() => void drain()}
        >
          Drain
        </Button>
      </div>
    </Panel>
  );
}

const imageKinds: Record<string, string> = {
  prebuilt: "Prebuilt — the checkout with setup already run",
  toolchain: "Toolchains only",
  unknown: "Baked before pier recorded how",
  "": "None — new sessions run the full setup",
};

function ImagePanel({ repo }: { repo: Repo }) {
  const path = repo.checkout?.path;
  const bakes = useRunningJobs((j) => j.kind === "bake" && j.repo === repo.name);
  const bake = async () => {
    if (!path) return;
    const j = await attempt(() => rpc.call("image.bake", { path }));
    if (j) trackJob(j);
  };
  return (
    <Panel
      title="Session image"
      actions={
        <Button
          size="sm"
          icon={<Hammer size={12} />}
          isDisabled={!path || repo.baking}
          onPress={() => void bake()}
        >
          {repo.baking ? "Baking…" : "Bake"}
        </Button>
      }
    >
      <Facts
        rows={[
          ["Image", imageKinds[repo.imageKind] ?? repo.imageKind],
          ["Baked", repo.bakedAt ? `${age(repo.bakedAt)} ago` : "—"],
        ]}
      />
      {repo.reminders.length > 0 && (
        <ul className="mt-3 flex flex-col gap-1.5">
          {repo.reminders.map((r) => (
            <li key={r.message} className="rounded-ctl bg-warn/10 px-2 py-1.5 text-xs text-label">
              {r.message}
            </li>
          ))}
        </ul>
      )}
      {bakes[0] && (
        <div className="mt-3">
          <JobProgress id={bakes[0].id} compact />
        </div>
      )}
      {!path && (
        <p className="mt-3 text-xs text-secondary">Bakes run from your local clone — locate it first.</p>
      )}
    </Panel>
  );
}

function CapacityPanel() {
  const q = useOverview()?.quota;
  return (
    <Panel title="Capacity">
      {q && q.limit > 0 ? (
        <div className="flex flex-col gap-2">
          <Meter value={q.used} max={q.limit} tone={q.used / q.limit > 0.85 ? "warn" : "accent"} />
          <div className="text-sm text-label">{q.detail}</div>
          <p className="text-xs text-secondary">
            vCPUs running in this region across the account, against its quota. Each running session uses its
            machine's vCPUs.
          </p>
        </div>
      ) : (
        <div className="text-sm text-secondary">Quota unknown — the account may not allow reading it.</div>
      )}
    </Panel>
  );
}
