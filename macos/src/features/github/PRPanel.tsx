import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { openUrl } from "@tauri-apps/plugin-opener";
import {
  BrushCleaning,
  ChevronRight,
  CircleCheck,
  CircleDashed,
  CircleSlash,
  CircleX,
  ExternalLink,
  GitMerge,
  GitPullRequest,
  MessageSquare,
  Wrench,
} from "lucide-react";
import { useState } from "react";
import { rpc } from "@/api/client";
import type { PRCheck, PRDetail, Session } from "@/api/protocol.gen";
import { RpcError } from "@/api/rpc";
import { inTauri } from "@/api/transport";
import { confirm } from "@/lib/confirm";
import { useBusy } from "@/lib/useBusy";
import { currentChat, showChat } from "@/state/currentChat";
import { useDrafts } from "@/state/drafts";
import { attempt, toast, toastError } from "@/state/toasts";
import { Badge } from "@/ui/Badge";
import { Button } from "@/ui/Button";
import { LogView } from "@/ui/LogView";
import { Nothing, rowClass, Section } from "@/ui/Section";
import { Spinner } from "@/ui/Spinner";
import { Markdown } from "../chat/Markdown";
import { cleanUp } from "./cleanup";
import { GitHubHTML } from "./GitHubHTML";

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

/**
 * A button that has pier write a prompt into the composer — which can take
 * a while (a failing check's logs come from GitHub) — saying so meanwhile.
 */
export function ComposeButton({
  session: s,
  kind,
  icon,
  variant,
  children,
}: {
  session: Session;
  kind: "fixCI" | "addressComments" | "createPR";
  icon?: React.ReactNode;
  variant?: "primary" | "secondary";
  children: React.ReactNode;
}) {
  const [busy, run] = useBusy();
  return (
    <Button
      size="sm"
      variant={variant}
      icon={icon}
      isPending={busy}
      onPress={() => void run(() => composeInto(s, kind))}
    >
      {children}
    </Button>
  );
}

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
  const onGitHub = /^[^/:]+\/[^/]+\/[^/]+$/.test(s.origin ?? "");
  if (!s.pr && onGitHub && !s.prChecked) {
    // Not asked yet: no pull request shown isn't "none".
    return (
      <div className="flex items-center justify-center gap-2 p-6 text-sm text-secondary">
        <Spinner size={12} /> Checking GitHub…
      </div>
    );
  }
  if (!s.pr) {
    return (
      <div className="flex flex-col items-center gap-3 p-6 text-center">
        <GitPullRequest size={22} strokeWidth={1.5} className="text-tertiary" />
        <div className="text-sm text-secondary">
          {onGitHub ? `No pull request for ${s.branch} yet.` : "This repository isn't on GitHub."}
        </div>
        {onGitHub && (
          <ComposeButton session={s} kind="createPR">
            Ask the agent to open one…
          </ComposeButton>
        )}
      </div>
    );
  }
  if (pr.error && !pr.data) {
    return <div className="selectable p-4 text-sm text-bad">{(pr.error as Error).message}</div>;
  }
  // What the overview has shows at once; checks and conversations follow.
  const shown = pr.data ?? { ...s.pr, id: "", body: "", runs: [], threads: [], reviews: [] };
  return <Detail session={s} pr={shown} loading={!pr.data} />;
}

function Detail({ session: s, pr, loading }: { session: Session; pr: PRDetail; loading: boolean }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const failing = pr.runs.filter((r) => r.state === "failing");
  const openThreads = pr.threads.filter((t) => !t.resolved);
  const asked = pr.reviews.some((r) => r.state === "changes_requested");
  const live = pr.state === "open" || pr.state === "draft";
  const review = reviewText[pr.review];
  const refresh = () => void qc.invalidateQueries({ queryKey: ["pr", s.id] });

  return (
    <div className="h-full overflow-y-auto">
      <div className="border-b border-separator px-4 pt-4 pb-4">
        <button
          type="button"
          onClick={() => open(pr.url)}
          className="text-left text-md leading-snug font-semibold hover:underline"
          title="Open on GitHub"
        >
          {pr.title} <span className="font-normal text-secondary">#{pr.number}</span>
        </button>
        <div className="mt-2.5 flex flex-wrap items-center gap-2 text-xs">
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
          <span className="text-secondary">
            into {pr.baseRef} · <span className="text-ok">+{pr.additions}</span>{" "}
            <span className="text-bad">−{pr.deletions}</span>
          </span>
        </div>
        <div className="mt-3.5 flex flex-wrap gap-2">
          {failing.length > 0 && live && (
            <ComposeButton session={s} kind="fixCI" variant="primary" icon={<Wrench size={11} />}>
              Fix CI…
            </ComposeButton>
          )}
          {(openThreads.length > 0 || asked) && live && (
            <ComposeButton session={s} kind="addressComments" icon={<MessageSquare size={11} />}>
              Address comments…
            </ComposeButton>
          )}
          <Button size="sm" icon={<ExternalLink size={11} />} onPress={() => open(pr.url)}>
            GitHub
          </Button>
        </div>
      </div>

      {pr.state === "merged" && (
        <Cleanup session={s} onRemoved={() => navigate({ to: "/repo/$name", params: { name: s.repo } })} />
      )}
      {pr.state === "open" && !loading && <Merge session={s} pr={pr} onMerged={refresh} />}

      <Section
        title={`Checks${
          loading
            ? pr.checksTotal
              ? ` · ${pr.checksPassed}/${pr.checksTotal} passing`
              : ""
            : pr.runs.length
              ? ` · ${pr.runs.filter((r) => r.state === "passing").length}/${pr.runs.length} passing`
              : ""
        }`}
      >
        {loading ? (
          <div className="flex items-center gap-2 py-1 text-sm text-secondary">
            <Spinner size={11} /> Loading checks…
          </div>
        ) : pr.runs.length === 0 ? (
          <Nothing>No checks ran on this head.</Nothing>
        ) : (
          pr.runs.map((r, i) => <CheckRow key={`${r.name}-${i}`} session={s} check={r} />)
        )}
      </Section>

      {openThreads.length > 0 && (
        <Section title="Open conversations" count={openThreads.length}>
          {openThreads.map((t, i) => (
            <div key={i} className="mt-3 rounded-card border border-separator px-3.5 py-3 first:mt-0">
              <div className="truncate font-mono text-[11px] text-tertiary">
                {t.path}
                {t.line ? `:${t.line}` : ""}
                {t.outdated && " · outdated"}
              </div>
              {t.comments.map((c, j) => (
                <div key={j} className="mt-2.5 text-sm">
                  <div className="mb-0.5 font-medium">{c.author}</div>
                  <div className="text-label/90">
                    {c.html ? <GitHubHTML html={c.html} /> : <Markdown text={c.body} />}
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
              <div
                key={i}
                className="mt-3 rounded-card border border-separator px-3.5 py-3 text-sm first:mt-0"
              >
                <div className="mb-1 flex items-center gap-2">
                  <span className="font-medium">{r.author}</span>
                  <span className="text-xs text-secondary">{r.state.replace("_", " ")}</span>
                </div>
                {r.html ? <GitHubHTML html={r.html} /> : <Markdown text={r.body} />}
              </div>
            ))}
        </Section>
      )}
    </div>
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
      <div className={`${rowClass} group text-sm hover:bg-hover`}>
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
        <div className="pt-1 pb-2">
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

function Merge({ session: s, pr, onMerged }: { session: Session; pr: PRDetail; onMerged: () => void }) {
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
      // Merges only what was shown here: if someone pushed since, GitHub
      // refuses. The method is the repository's default, as on GitHub.
      await rpc.call("pr.merge", { sessionId: s.id, expectedHeadOid: pr.headOid });
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
    <div className="flex items-center gap-3 border-b border-separator px-4 py-3">
      <span className="min-w-0 flex-1 text-sm text-secondary">
        {pr.mergeable === "clean" ? "Ready to merge" : mergeText[pr.mergeable]}
      </span>
      <Button
        size="sm"
        variant="primary"
        icon={<GitMerge size={11} />}
        isPending={busy}
        isDisabled={blocked}
        onPress={() => void merge()}
      >
        {busy ? "Merging…" : "Merge"}
      </Button>
    </div>
  );
}

/** Cleaning up leaves the page at once: the session's row says it's going. */
function Cleanup({ session: s, onRemoved }: { session: Session; onRemoved: () => void }) {
  return (
    <div className="flex items-center gap-3 border-b border-separator bg-merged/8 px-4 py-3 text-sm">
      <GitMerge size={14} className="shrink-0 text-merged" />
      <span className="flex-1">Merged. Clean it up once nothing in it is left behind.</span>
      <Button size="sm" icon={<BrushCleaning size={11} />} onPress={() => void cleanUp(s, onRemoved)}>
        Clean up
      </Button>
    </div>
  );
}
