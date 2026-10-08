import { useNavigate, useRouterState } from "@tanstack/react-router";
import { ChevronRight, House, Plus, Settings } from "lucide-react";
import type { ReactNode } from "react";
import { Button as AriaButton } from "react-aria-components";
import type { Repo, Session } from "@/api/protocol.gen";
import { PRBadge } from "@/features/github/PRBadge";
import { linkCheckout } from "@/features/repos/link";
import { hourly } from "@/lib/format";
import { attentionOf, reasonText, reasonTone, useSeen } from "@/state/attention";
import { sessionTone, useOverview } from "@/state/overview";
import { useUI } from "@/state/ui";
import { IconButton } from "@/ui/Button";
import { Meter } from "@/ui/Panel";
import { StateDot } from "@/ui/StateDot";

function Row({
  active,
  onPress,
  indent = 0,
  children,
  leading,
  trailing,
  label,
}: {
  active: boolean;
  onPress: () => void;
  indent?: number;
  children: ReactNode;
  leading?: ReactNode;
  trailing?: ReactNode;
  label: string;
}) {
  return (
    <div className="group relative">
      {leading && <div className="absolute top-0 left-0.5 z-10 flex h-7 items-center">{leading}</div>}
      <AriaButton
        aria-label={label}
        aria-current={active ? "page" : undefined}
        onPress={onPress}
        style={{ paddingLeft: 8 + indent * 14 }}
        className={`flex h-7 w-full items-center gap-2 rounded-ctl pr-2 text-left text-base outline-none focus-visible:ring-2 focus-visible:ring-accent/50 ${active ? "bg-selected font-medium text-label" : "text-label"}`}
      >
        {children}
      </AriaButton>
      {trailing && <div className="absolute top-0 right-1 flex h-7 items-center">{trailing}</div>}
    </div>
  );
}

const hintTone: Record<string, string> = {
  warn: "font-medium text-warn",
  bad: "text-bad",
  ok: "text-ok",
  merged: "text-merged",
  accent: "text-accent",
};

function sessionHint(s: Session, seen: Record<string, string>): [string, string] {
  const reason = attentionOf(s, seen);
  if (reason && reason !== "finished") return [reasonText[reason], hintTone[reasonTone[reason]] ?? ""];
  const plain = plainHint(s);
  return [plain, plain.includes("fail") ? "text-bad" : "text-tertiary"];
}

function plainHint(s: Session): string {
  if (s.state === "failed") return "failed";
  if (s.state === "creating") return "starting";
  if (s.state === "parked") return "parked";
  if (s.state === "deleting") return "deleting";
  if (s.setup === "running") return "setup";
  if (s.setup === "failed") return "setup failed";
  return "";
}

function RepoGroup({ repo, sessions, path }: { repo: Repo; sessions: Session[]; path: string }) {
  const navigate = useNavigate();
  const collapsed = useUI((s) => !!s.collapsed[repo.name]);
  const toggle = useUI((s) => s.toggleRepo);
  const openNew = useUI((s) => s.openNewSession);
  const seen = useSeen((s) => s.seen);
  const repoPath = `/repo/${encodeURIComponent(repo.name)}`;
  const pool = repo.poolTarget > 0 || repo.poolReady > 0 || repo.poolFilling > 0;
  return (
    <div className="mt-0.5">
      <Row
        label={`Repository ${repo.name}`}
        active={path === repoPath}
        onPress={() => navigate({ to: "/repo/$name", params: { name: repo.name } })}
        trailing={
          <IconButton
            label={`New session in ${repo.name}`}
            className="size-6 opacity-0 group-hover:opacity-100 focus-visible:opacity-100"
            onPress={() => openNew(repo.name)}
          >
            <Plus size={14} />
          </IconButton>
        }
        indent={1}
        leading={
          <button
            type="button"
            aria-label={collapsed ? `Show ${repo.name}'s sessions` : `Hide ${repo.name}'s sessions`}
            aria-expanded={!collapsed}
            onClick={() => toggle(repo.name)}
            className="flex size-5 items-center justify-center rounded text-tertiary hover:text-label"
          >
            <ChevronRight size={12} className={`transition-transform ${collapsed ? "" : "rotate-90"}`} />
          </button>
        }
      >
        <span className="min-w-0 flex-1 truncate font-semibold">{repo.name}</span>
        {repo.reminders.length > 0 && (
          <span className="size-1.5 rounded-full bg-warn" title="Rebake suggested" />
        )}
      </Row>
      {!collapsed && (
        <div>
          {sessions.map((s) => {
            const p = `/session/${encodeURIComponent(s.id)}`;
            const [hint, hintClass] = sessionHint(s, seen);
            const unread = attentionOf(s, seen) === "finished";
            return (
              <Row
                key={s.id}
                indent={2}
                label={`Session ${s.name}`}
                active={path === p}
                onPress={() => navigate({ to: "/session/$id", params: { id: s.id } })}
              >
                <StateDot tone={sessionTone(s)} />
                <span
                  className={`min-w-0 flex-1 truncate ${s.state === "parked" ? "text-secondary" : ""} ${unread ? "font-semibold" : ""}`}
                >
                  {s.name}
                </span>
                {hint ? (
                  <span className={`shrink-0 text-xs ${hintClass}`}>{hint}</span>
                ) : (
                  s.pr && <PRBadge pr={s.pr} />
                )}
                {unread && !hint && (
                  <span className="size-1.5 shrink-0 rounded-full bg-accent" title="Finished" />
                )}
              </Row>
            );
          })}
          {pool && (
            <div
              className="flex h-6 items-center gap-2 pl-[44px] text-xs text-tertiary"
              title="Pooled sessions: parked and set up, claimed by the next new session"
            >
              pool {repo.poolReady}/{repo.poolTarget} ready
              {repo.poolFilling > 0 ? ` · ${repo.poolFilling} filling` : ""}
            </div>
          )}
          {sessions.length === 0 && !pool && (
            <div className="h-6 pl-[44px] text-xs text-tertiary">no sessions</div>
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
          <House size={15} className="text-secondary" />
          <span className="flex-1">Home</span>
        </Row>
        <div className="mt-4 mb-1 flex h-5 items-center px-2">
          <span className="flex-1 text-xs font-semibold text-tertiary">Repositories</span>
          <IconButton label="Link a repository" className="size-5" onPress={() => void linkCheckout()}>
            <Plus size={13} />
          </IconButton>
        </div>
        {repos.map((r) => (
          <RepoGroup key={r.name} repo={r} sessions={byRepo.get(r.name) ?? []} path={path} />
        ))}
        {o && repos.length === 0 && (
          <div className="px-2 py-2 text-xs text-secondary">
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
  const q = o?.quota;
  return (
    <div className="shrink-0 border-t border-separator px-3 py-2">
      <div className="flex items-center gap-2 text-xs text-secondary">
        <span className="min-w-0 flex-1 truncate" title={o?.cloud}>
          {o ? `${o.totals.running} running · ${hourly(o.totals.hourlyUsd)}` : "connecting…"}
        </span>
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
