import { useNavigate, useRouterState } from "@tanstack/react-router";
import {
  BrushCleaning,
  ChevronRight,
  CircleAlert,
  CircleX,
  GitMerge,
  GitPullRequest,
  GitPullRequestDraft,
  House,
  LoaderCircle,
  MessageCircleQuestion,
  MessageSquareWarning,
  Plus,
  Settings,
  TriangleAlert,
} from "lucide-react";
import { type ReactNode, useEffect } from "react";
import { Button as AriaButton } from "react-aria-components";
import type { Repo, Session } from "@/api/protocol.gen";
import { cleanUp } from "@/features/github/cleanup";
import { linkCheckout } from "@/features/repos/link";
import { hourly } from "@/lib/format";
import { attentionOf, useSeen } from "@/state/attention";
import { sessionTone, useOverview } from "@/state/overview";
import { useProxy } from "@/state/proxy";
import { settleRemoving, useRemoving } from "@/state/removing";
import { useUI } from "@/state/ui";
import { IconButton } from "@/ui/Button";
import { Meter } from "@/ui/Panel";
import { Spinner } from "@/ui/Spinner";
import { StateDot } from "@/ui/StateDot";
import { HoverTip } from "@/ui/Tooltip";

function Row({
  active,
  onPress,
  indent = 0,
  height = "h-9",
  children,
  leading,
  trailing,
  label,
}: {
  active: boolean;
  onPress: () => void;
  indent?: number;
  height?: string;
  children: ReactNode;
  leading?: ReactNode;
  trailing?: ReactNode;
  label: string;
}) {
  return (
    <div className="group relative">
      {leading && (
        <div
          className={`absolute top-0 left-0 z-10 flex items-center ${height}`}
          style={{ paddingLeft: 10 + indent * 16 }}
        >
          {leading}
        </div>
      )}
      <AriaButton
        aria-label={label}
        aria-current={active ? "page" : undefined}
        onPress={onPress}
        style={{ paddingLeft: 10 + indent * 16 }}
        className={`flex w-full items-center gap-2.5 rounded-ctl text-left text-base outline-none focus-visible:ring-2 focus-visible:ring-accent/50 ${height} ${trailing ? "pr-10" : "pr-2.5"} ${active ? "bg-selected text-label" : "text-label hover:bg-hover"}`}
      >
        {children}
      </AriaButton>
      {trailing && <div className={`absolute top-0 right-1.5 flex items-center ${height}`}>{trailing}</div>}
    </div>
  );
}

/**
 * A merged session's mark, which turns into its clean-up on hover: the VM
 * and disk go, so it stops costing anything.
 */
function CleanUpButton({ session: s, onStart }: { session: Session; onStart: () => void }) {
  return (
    <HoverTip content={`#${s.pr?.number} merged - Clean up VM & disk`}>
      <IconButton label={`Clean up ${s.name}`} size="sm" onPress={() => void cleanUp(s, onStart)}>
        <GitMerge {...prIcon} className="text-merged group-hover:hidden group-focus-within:hidden" />
        <BrushCleaning {...prIcon} className="hidden group-hover:block group-focus-within:block" />
      </IconButton>
    </HoverTip>
  );
}

interface Status {
  icon: ReactNode;
  /** What the icon means, in words: its tooltip, and part of the row's label. */
  text: string;
}

const prIcon = { size: 15, strokeWidth: 2 } as const;

/** One icon for what most needs saying about a session, if anything does. */
function sessionStatus(s: Session, seen: Record<string, string>): Status | undefined {
  const pr = s.pr ? `#${s.pr.number}` : "its pull request";
  switch (attentionOf(s, seen)) {
    case "needs_input":
      return {
        icon: <MessageCircleQuestion {...prIcon} className="text-warn" />,
        text: "Waiting on your answer",
      };
    case "create_failed":
      return { icon: <TriangleAlert {...prIcon} className="text-bad" />, text: "Couldn't be created" };
    case "setup_failed":
      return {
        icon: <TriangleAlert {...prIcon} className="text-bad" />,
        text: "Setup failed — its log is in the Setup tab",
      };
    case "ci_failing":
      return { icon: <CircleX {...prIcon} className="text-bad" />, text: `CI is failing on ${pr}` };
    case "changes_requested":
      return {
        icon: <MessageSquareWarning {...prIcon} className="text-warn" />,
        text: `Changes requested on ${pr}`,
      };
    case "finished":
      return {
        icon: <span className="mx-1 size-2 rounded-full bg-accent" />,
        text: "Finished — not looked at yet",
      };
    case "ready_to_merge":
      return { icon: <GitMerge {...prIcon} className="text-ok" />, text: `${pr} is ready to merge` };
    case "merged_cleanup":
      return {
        icon: <GitMerge {...prIcon} className="text-merged" />,
        text: `${pr} merged — the session can go`,
      };
  }
  if (s.state === "failed")
    return { icon: <TriangleAlert {...prIcon} className="text-bad" />, text: "Failed" };
  if (s.setup === "running")
    return {
      icon: <LoaderCircle {...prIcon} className="animate-spin text-secondary" />,
      text: "Running its setup",
    };
  if (s.pr && (s.pr.state === "open" || s.pr.state === "draft")) {
    const checks =
      s.pr.checks === "pending"
        ? "checks running"
        : s.pr.checks === "passing"
          ? "checks passing"
          : s.pr.checks
            ? `checks ${s.pr.checks}`
            : "no checks";
    const Icon = s.pr.state === "draft" ? GitPullRequestDraft : GitPullRequest;
    const tone =
      s.pr.checks === "pending" ? "text-warn" : s.pr.state === "draft" ? "text-tertiary" : "text-ok";
    return { icon: <Icon {...prIcon} className={tone} />, text: `#${s.pr.number} ${s.pr.title} — ${checks}` };
  }
  return undefined;
}

const stateText: Record<string, string> = {
  creating: "starting",
  working: "working",
  running: "running",
  idle: "idle",
  parked: "parked",
  deleting: "being removed",
  failed: "failed",
};

/** One icon for a repo that can't do what it should, if any. */
function repoStatus(r: Repo): Status | undefined {
  if (r.needsImage && r.baking)
    return {
      icon: <LoaderCircle {...prIcon} className="animate-spin text-secondary" />,
      text: "Baking its first session image — sessions can start once it's done",
    };
  if (r.needsImage)
    return {
      icon: <CircleAlert {...prIcon} className="text-warn" />,
      text: "Needs its session image before sessions can start: its .pier/bake.sh asks for toolchains",
    };
  if (r.baking)
    return {
      icon: <LoaderCircle {...prIcon} className="animate-spin text-secondary" />,
      text: "Baking a new session image",
    };
  if (r.poolError && r.poolReady < r.poolTarget && r.poolFilling === 0)
    return {
      icon: <TriangleAlert {...prIcon} className="text-warn" />,
      text: `The pool's last fill failed: ${r.poolError.message}`,
    };
  if (r.reminders[0])
    return { icon: <span className="mx-1 size-2 rounded-full bg-warn" />, text: r.reminders[0].message };
  return undefined;
}

function RepoGroup({ repo, sessions, path }: { repo: Repo; sessions: Session[]; path: string }) {
  const navigate = useNavigate();
  const collapsed = useUI((s) => !!s.collapsed[repo.name]);
  const toggle = useUI((s) => s.toggleRepo);
  const openNew = useUI((s) => s.openNewSession);
  const seen = useSeen((s) => s.seen);
  const removing = useRemoving((r) => r.ids);
  const repoPath = `/repo/${encodeURIComponent(repo.name)}`;
  const status = repoStatus(repo);
  return (
    <div className="mt-1.5">
      <Row
        label={`Repository ${repo.name}${status ? `: ${status.text}` : ""}`}
        height="h-10"
        active={path === repoPath}
        onPress={() => navigate({ to: "/repo/$name", params: { name: repo.name } })}
        trailing={
          <IconButton
            label={`New session in ${repo.name}`}
            className="size-7 opacity-0 group-hover:opacity-100 focus-visible:opacity-100"
            onPress={() => openNew(repo.name)}
          >
            <Plus size={15} />
          </IconButton>
        }
        leading={
          <button
            type="button"
            aria-label={collapsed ? `Show ${repo.name}'s sessions` : `Hide ${repo.name}'s sessions`}
            aria-expanded={!collapsed}
            onClick={() => toggle(repo.name)}
            className="flex size-[22px] items-center justify-center rounded-[6px] bg-selected text-xs font-semibold text-secondary uppercase hover:text-label"
          >
            <span className="group-hover:hidden">{repo.name.slice(0, 1)}</span>
            <ChevronRight
              size={13}
              className={`hidden transition-transform group-hover:block ${collapsed ? "" : "rotate-90"}`}
            />
          </button>
        }
      >
        <span className="w-[22px] shrink-0" />
        <span className="min-w-0 flex-1 truncate text-[14px] font-semibold">{repo.name}</span>
        {status && <HoverTip content={status.text}>{status.icon}</HoverTip>}
      </Row>
      {!collapsed && (
        <div className="mt-0.5 flex flex-col gap-px">
          {sessions.map((s) => {
            const p = `/session/${encodeURIComponent(s.id)}`;
            const st = sessionStatus(s, seen);
            const merged = s.pr?.state === "merged";
            const going = removing[s.id];
            return (
              <Row
                key={s.id}
                indent={1}
                label={`Session ${s.name}, ${going ?? `${stateText[s.state] ?? s.state}${st ? `: ${st.text}` : ""}`}`}
                active={path === p}
                onPress={() => navigate({ to: "/session/$id", params: { id: s.id } })}
                trailing={
                  going ? (
                    <HoverTip content={going}>
                      <span className="flex size-6 items-center justify-center">
                        <Spinner size={12} />
                      </span>
                    </HoverTip>
                  ) : merged ? (
                    <CleanUpButton
                      session={s}
                      onStart={() => {
                        if (path === p) navigate({ to: "/repo/$name", params: { name: s.repo } });
                      }}
                    />
                  ) : undefined
                }
              >
                <span className="flex w-[22px] shrink-0 justify-center">
                  <StateDot tone={sessionTone(s)} />
                </span>
                <span
                  className={`min-w-0 flex-1 truncate ${s.state === "parked" || going ? "text-secondary" : ""}`}
                >
                  {s.name}
                </span>
                {st && !merged && !going && <HoverTip content={st.text}>{st.icon}</HoverTip>}
              </Row>
            );
          })}
          {sessions.length === 0 && (
            <div className="flex h-8 items-center pl-[58px] text-sm text-tertiary">No sessions</div>
          )}
        </div>
      )}
    </div>
  );
}

export function Sidebar() {
  const o = useOverview();
  const navigate = useNavigate();
  const path = useRouterState({ select: (s) => s.location.pathname });
  // A session that went is no longer going.
  useEffect(() => {
    if (o) settleRemoving(new Set(o.sessions.map((s) => s.id)));
  }, [o]);
  const repos = o?.repos ?? [];
  const byRepo = new Map<string, Session[]>();
  for (const s of o?.sessions ?? []) {
    if (s.pooled) continue;
    byRepo.set(s.repo, [...(byRepo.get(s.repo) ?? []), s]);
  }
  for (const list of byRepo.values()) list.sort((a, b) => a.name.localeCompare(b.name));
  return (
    <nav aria-label="Repositories and sessions" className="flex h-full flex-col">
      <div className="min-h-0 flex-1 overflow-y-auto px-2 pb-2">
        <Row label="Home" active={path === "/"} onPress={() => navigate({ to: "/" })}>
          <span className="flex w-[22px] shrink-0 justify-center">
            <House size={16} className="text-secondary" />
          </span>
          <span className="flex-1">Home</span>
        </Row>
        <div className="mt-5 mb-0.5 flex h-7 items-center pr-1.5 pl-2.5">
          <span className="flex-1 text-xs font-semibold text-tertiary">Repositories</span>
          <IconButton label="Link a repository" className="size-7" onPress={() => void linkCheckout()}>
            <Plus size={15} />
          </IconButton>
        </div>
        {repos.map((r) => (
          <RepoGroup key={r.name} repo={r} sessions={byRepo.get(r.name) ?? []} path={path} />
        ))}
        {o && repos.length === 0 && (
          <div className="px-2.5 py-2 text-sm text-secondary">
            Link a local clone to create sessions from it.
          </div>
        )}
      </div>
      <SidebarFooter />
    </nav>
  );
}

function SidebarFooter() {
  const o = useOverview();
  const navigate = useNavigate();
  const proxy = useProxy();
  const q = o?.quota;
  return (
    <div className="shrink-0 border-t border-separator px-3 py-2">
      <div className="flex items-center gap-2 text-xs text-secondary">
        <span className="min-w-0 flex-1 truncate" title={o?.cloud}>
          {o ? `${o.totals.running} running · ${hourly(o.totals.hourlyUsd)}` : "connecting…"}
        </span>
        {proxy?.running && (
          <button
            type="button"
            onClick={() => navigate({ to: "/settings" })}
            title={`Local names on: ${proxy.sessions.map((x) => x.host).join(", ") || "no running sessions"}`}
            className="flex h-5 shrink-0 items-center gap-1 rounded-full bg-accent-soft px-2 text-2xs font-medium text-accent"
          >
            .pier
          </button>
        )}
        <IconButton label="Settings" className="size-6" onPress={() => navigate({ to: "/settings" })}>
          <Settings size={14} />
        </IconButton>
      </div>
      {q && q.limit > 0 && (
        <div className="mt-1" title={q.detail}>
          <Meter value={q.used} max={q.limit} tone={q.used / q.limit > 0.85 ? "warn" : "accent"} />
        </div>
      )}
    </div>
  );
}
