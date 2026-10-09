import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { openUrl } from "@tauri-apps/plugin-opener";
import {
  ChevronRight,
  CircleCheck,
  CircleDashed,
  CircleSlash,
  CircleX,
  ExternalLink,
  GitMerge,
  GitPullRequest,
  MessageSquare,
  RotateCw,
  Trash2,
  Wrench,
} from "lucide-react";
import { useState } from "react";
import { rpc } from "@/api/client";
import type { PRCheck, PRDetail, Session } from "@/api/protocol.gen";
import { RpcError } from "@/api/rpc";
import { inTauri } from "@/api/transport";
import { confirm } from "@/lib/confirm";
import { currentChat, showChat } from "@/state/currentChat";
import { useDrafts } from "@/state/drafts";
import { attempt, toast, toastError } from "@/state/toasts";
import { Badge } from "@/ui/Badge";
import { Button } from "@/ui/Button";
import { LogView } from "@/ui/LogView";
import { Spinner } from "@/ui/Spinner";
import { Markdown } from "../chat/Markdown";

function open(url: string) {
  if (inTauri) void openUrl(url);
  else window.open(url, "_blank", "noopener");
}

/** Puts a prompt pier wrote in the current chat's composer, for you to read and send. */
async function composeInto(s: Session, kind: "fixCI" | "addressComments" | "createPR") {
  const r = await attempt(() => rpc.call("chat.compose", { sessionId: s.id, kind }));
  if (!r) return;
  const t = currentChat(s);
  useDrafts.getState().setDraft(`${s.id}/${t.chatId}`, r.text);
  showChat(s.id, t.chatId);
  toast("In the message box — read it over and send", "info");
}

export { composeInto };

const reviewText: Record<string, [string, "ok" | "warn" | "neutral"]> = {
  approved: ["approved", "ok"],
  changes_requested: ["changes requested", "warn"],
  review_required: ["review required", "neutral"],
};

const mergeText: Record<string, string> = {
  clean: "ready to merge",
  conflicts: "has conflicts",
  blocked: "blocked by branch rules",
  behind: "behind its base",
  unstable: "checks failing",
  unknown: "mergeability unknown",
};

/** The session's pull request: its checks, its review, and merging it. */
export function PRPanel({ session: s }: { session: Session }) {
  const pr = useQuery({
    queryKey: ["pr", s.id],
    queryFn: () => rpc.call("pr.get", { id: s.id }),
    enabled: !!s.pr,
    refetchInterval: s.pr?.checks === "pending" ? 15_000 : 45_000,
    retry: false,
  });
  if (!s.pr) {
    const onGitHub = /^[^/:]+\/[^/]+\/[^/]+$/.test(s.origin ?? "");
    return (
      <div className="flex flex-col items-center gap-3 p-6 text-center">
        <GitPullRequest size={22} strokeWidth={1.5} className="text-tertiary" />
        <div className="text-sm text-secondary">
          {onGitHub ? `No pull request for ${s.branch} yet.` : "This repository isn't on GitHub."}
        </div>
        {onGitHub && (
          <Button size="sm" onPress={() => void composeInto(s, "createPR")}>
            Ask the agent to open one…
          </Button>
        )}
      </div>
    );
  }
  if (pr.error && !pr.data) {
    return <div className="selectable p-4 text-sm text-bad">{(pr.error as Error).message}</div>;
  }
  if (!pr.data) {
    return (
      <div className="flex justify-center p-6">
        <Spinner />
      </div>
    );
  }
  return <Detail session={s} pr={pr.data} />;
}

function Detail({ session: s, pr }: { session: Session; pr: PRDetail }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const failing = pr.runs.filter((r) => r.state === "failing");
  const runs = [...new Set(failing.map((r) => r.runId).filter(Boolean))] as number[];
  const openThreads = pr.threads.filter((t) => !t.resolved);
  const asked = pr.reviews.some((r) => r.state === "changes_requested");
  const live = pr.state === "open" || pr.state === "draft";
  const review = reviewText[pr.review];
  const refresh = () => void qc.invalidateQueries({ queryKey: ["pr", s.id] });

  return (
    <div className="h-full overflow-y-auto">
      <div className="border-b border-separator px-3 py-3">
        <button
          type="button"
          onClick={() => open(pr.url)}
          className="text-left text-base font-medium hover:underline"
          title="Open on GitHub"
        >
          {pr.title} <span className="font-normal text-secondary">#{pr.number}</span>
        </button>
        <div className="mt-1.5 flex flex-wrap items-center gap-1.5 text-xs">
          <Badge
            tone={
              pr.state === "merged"
                ? "merged"
                : pr.state === "closed"
                  ? "bad"
                  : pr.state === "draft"
                    ? "neutral"
                    : "ok"
            }
          >
            {pr.state}
          </Badge>
          {review && live && <Badge tone={review[1]}>{review[0]}</Badge>}
          {live && pr.mergeable !== "clean" && (
            <span className="text-secondary">{mergeText[pr.mergeable]}</span>
          )}
          <span className="text-secondary">
            into {pr.baseRef} · <span className="text-ok">+{pr.additions}</span>{" "}
            <span className="text-bad">−{pr.deletions}</span>
          </span>
        </div>
        <div className="mt-2.5 flex flex-wrap gap-1.5">
          {failing.length > 0 && live && (
            <Button
              size="sm"
              variant="primary"
              icon={<Wrench size={11} />}
              onPress={() => void composeInto(s, "fixCI")}
            >
              Fix CI…
            </Button>
          )}
          {(openThreads.length > 0 || asked) && live && (
            <Button
              size="sm"
              icon={<MessageSquare size={11} />}
              onPress={() => void composeInto(s, "addressComments")}
            >
              Address comments…
            </Button>
          )}
          {runs.length > 0 && live && (
            <Button
              size="sm"
              icon={<RotateCw size={11} />}
              onPress={async () => {
                for (const runId of runs)
                  await attempt(() => rpc.call("pr.rerun", { sessionId: s.id, runId }));
                toast("Re-running the failed jobs", "ok");
                setTimeout(refresh, 3000);
              }}
            >
              Re-run failed
            </Button>
          )}
          <Button size="sm" icon={<ExternalLink size={11} />} onPress={() => open(pr.url)}>
            GitHub
          </Button>
        </div>
      </div>

      {pr.state === "merged" && (
        <Cleanup session={s} onRemoved={() => navigate({ to: "/repo/$name", params: { name: s.repo } })} />
      )}
      {pr.state === "open" && <Merge session={s} pr={pr} onMerged={refresh} />}

      <Section
        title={`Checks${pr.runs.length ? ` · ${pr.runs.filter((r) => r.state === "passing").length}/${pr.runs.length} passing` : ""}`}
      >
        {pr.runs.length === 0 ? (
          <div className="px-3 py-1 text-sm text-secondary">No checks ran on this head.</div>
        ) : (
          pr.runs.map((r, i) => <CheckRow key={`${r.name}-${i}`} session={s} check={r} />)
        )}
      </Section>

      {openThreads.length > 0 && (
        <Section title={`Open conversations · ${openThreads.length}`}>
          {openThreads.map((t, i) => (
            <div key={i} className="border-t border-separator px-3 py-2 first:border-t-0">
              <div className="truncate font-mono text-[11px] text-secondary">
                {t.path}
                {t.line ? `:${t.line}` : ""}
                {t.outdated && " · outdated"}
              </div>
              {t.comments.map((c, j) => (
                <div key={j} className="mt-1 text-sm">
                  <span className="font-medium">{c.author}</span>
                  <div className="text-label/90">
                    <Markdown text={c.body} />
                  </div>
                </div>
              ))}
            </div>
          ))}
        </Section>
      )}

      {pr.reviews.some((r) => r.body.trim()) && (
        <Section title="Reviews">
          {pr.reviews
            .filter((r) => r.body.trim())
            .map((r, i) => (
              <div key={i} className="border-t border-separator px-3 py-2 text-sm first:border-t-0">
                <div className="flex items-center gap-1.5">
                  <span className="font-medium">{r.author}</span>
                  <span className="text-xs text-secondary">{r.state.replace("_", " ")}</span>
                </div>
                <Markdown text={r.body} />
              </div>
            ))}
        </Section>
      )}
    </div>
  );
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="border-b border-separator py-2">
      <h3 className="px-3 pb-1 text-xs font-semibold text-secondary">{title}</h3>
      {children}
    </section>
  );
}

const checkIcon = {
  passing: <CircleCheck size={13} className="shrink-0 text-ok" />,
  failing: <CircleX size={13} className="shrink-0 text-bad" />,
  pending: <CircleDashed size={13} className="shrink-0 animate-spin-slow text-warn" />,
  skipped: <CircleSlash size={13} className="shrink-0 text-tertiary" />,
} as const;

function CheckRow({ session: s, check: c }: { session: Session; check: PRCheck }) {
  const [expanded, setExpanded] = useState(false);
  const log = useQuery({
    queryKey: ["pr-log", s.id, c.jobId],
    queryFn: () => rpc.call("pr.log", { sessionId: s.id, jobId: c.jobId! }),
    enabled: expanded && !!c.jobId,
    staleTime: 60_000,
  });
  return (
    <div>
      <div className="group flex h-7 items-center gap-2 px-3 text-sm hover:bg-hover">
        {checkIcon[c.state as keyof typeof checkIcon] ?? checkIcon.pending}
        <button
          type="button"
          disabled={!c.jobId}
          onClick={() => setExpanded(!expanded)}
          className="flex min-w-0 flex-1 items-center gap-1 text-left disabled:cursor-default"
        >
          <span className="truncate">{c.name}</span>
          {c.jobId ? (
            <ChevronRight
              size={11}
              className={`shrink-0 text-tertiary transition-transform ${expanded ? "rotate-90" : ""}`}
            />
          ) : null}
        </button>
        {c.url && (
          <button
            type="button"
            aria-label={`Open ${c.name}`}
            onClick={() => open(c.url)}
            className="text-tertiary opacity-0 hover:text-label group-hover:opacity-100"
          >
            <ExternalLink size={11} />
          </button>
        )}
      </div>
      {expanded && (
        <div className="px-3 pb-2">
          {log.data ? (
            <LogView text={log.data.text} className="max-h-72" empty="empty log" />
          ) : log.error ? (
            <div className="text-xs text-bad">{(log.error as Error).message}</div>
          ) : (
            <Spinner />
          )}
        </div>
      )}
    </div>
  );
}

const methods = [
  ["squash", "Squash and merge"],
  ["merge", "Merge commit"],
  ["rebase", "Rebase and merge"],
] as const;

function Merge({ session: s, pr, onMerged }: { session: Session; pr: PRDetail; onMerged: () => void }) {
  const [method, setMethod] = useState<(typeof methods)[number][0]>(
    () => (localStorage.getItem("pier.mergeMethod") as "squash") ?? "squash",
  );
  const [busy, setBusy] = useState(false);
  const blocked = pr.mergeable === "conflicts" || pr.mergeable === "blocked";
  const merge = async () => {
    const pending = pr.runs.some((r) => r.state === "pending");
    const failing = pr.runs.some((r) => r.state === "failing");
    if (
      (pending || failing || pr.review !== "approved") &&
      !(await confirm(
        `Merge #${pr.number}?`,
        [
          failing ? "Some checks are failing." : pending ? "Some checks are still running." : "",
          pr.review !== "approved" ? "It hasn't been approved." : "",
        ]
          .filter(Boolean)
          .join(" "),
        { ok: "Merge anyway", destructive: true },
      ))
    )
      return;
    setBusy(true);
    try {
      // Merges only what was shown here: if someone pushed since, GitHub refuses.
      await rpc.call("pr.merge", { sessionId: s.id, method, expectedHeadOid: pr.headOid });
      localStorage.setItem("pier.mergeMethod", method);
      toast(`Merged #${pr.number}`, "ok");
      onMerged();
    } catch (e) {
      if (e instanceof RpcError && e.kind === "head_moved") {
        toast("New commits landed since this was loaded — look again before merging", "bad");
        onMerged();
      } else toastError(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="flex items-center gap-1.5 border-b border-separator px-3 py-2.5">
      <select
        aria-label="Merge method"
        value={method}
        onChange={(e) => setMethod(e.target.value as typeof method)}
        className="h-[22px] min-w-0 flex-1 rounded-ctl border border-separator bg-control px-1.5 text-xs outline-none"
      >
        {methods.map(([m, label]) => (
          <option key={m} value={m}>
            {label}
          </option>
        ))}
      </select>
      <Button
        size="sm"
        variant="primary"
        icon={<GitMerge size={11} />}
        isDisabled={busy || blocked}
        onPress={() => void merge()}
      >
        {busy ? "Merging…" : "Merge"}
      </Button>
    </div>
  );
}

function Cleanup({ session: s, onRemoved }: { session: Session; onRemoved: () => void }) {
  const [busy, setBusy] = useState(false);
  return (
    <div className="flex items-center gap-2 border-b border-separator bg-merged/8 px-3 py-2.5 text-sm">
      <GitMerge size={14} className="shrink-0 text-merged" />
      <span className="flex-1">Merged. Remove the session once nothing in it is left behind.</span>
      <Button
        size="sm"
        icon={<Trash2 size={11} />}
        isDisabled={busy}
        onPress={async () => {
          if (
            !(await confirm(`Remove ${s.name}?`, "Its pull request merged; the VM and its disk go.", {
              ok: "Remove",
              destructive: true,
            }))
          )
            return;
          setBusy(true);
          const ok = await attempt(() => rpc.call("sessions.removeMerged", { id: s.id }));
          setBusy(false);
          if (ok !== undefined) {
            toast(`Removed ${s.name}`, "ok");
            onRemoved();
          }
        }}
      >
        Clean up
      </Button>
    </div>
  );
}
