import { ArrowUpFromLine, GitBranch, GitCommitHorizontal, GitPullRequest, Send } from "lucide-react";
import { useState } from "react";
import { rpc } from "@/api/client";
import { useWatch } from "@/api/live";
import type { Session } from "@/api/protocol.gen";
import { composeInto } from "@/features/github/PRPanel";
import { plural } from "@/lib/format";
import { reviewMessage, useComments } from "@/state/comments";
import { sendToAgent } from "@/state/currentChat";
import { docId, useDocs } from "@/state/docs";
import { attempt, toast } from "@/state/toasts";
import { Button } from "@/ui/Button";
import { Spinner } from "@/ui/Spinner";
import { CommitSheet } from "./CommitSheet";
import { StatusMark, splitPath, totals } from "./status";

/** What the session's branch changed against its base, and the ways to ship it. */
export function ChangesPanel({ session: s }: { session: Session }) {
  // Pushed by the session as its checkout changes.
  const changes = useWatch("git", { id: s.id });
  const open = useDocs((d) => d.open);
  const activeDoc = useDocs((d) => d.active[s.id]);
  const comments = useComments((c) => c.comments[s.id]?.length ?? 0);
  const [committing, setCommitting] = useState(false);
  const [pushing, setPushing] = useState(false);

  if (changes.error && !changes.data) {
    return <div className="selectable p-4 text-sm text-secondary">{(changes.error as Error).message}</div>;
  }
  const c = changes.data;
  if (!c) {
    return (
      <div className="flex justify-center p-6">
        <Spinner />
      </div>
    );
  }
  const { added, removed } = totals(c.files);
  const unpushed = c.upstream ? c.ahead : undefined;
  const push = async () => {
    setPushing(true);
    const ok = await attempt(() => rpc.call("git.push", { sessionId: s.id, opId: crypto.randomUUID() }));
    setPushing(false);
    if (ok !== undefined) toast(`Pushed ${c.branch || "the branch"}`, "ok");
  };
  return (
    <div className="flex h-full flex-col">
      <div className="shrink-0 border-b border-separator px-3 py-2.5">
        <div className="flex items-center gap-1.5 text-sm">
          <GitBranch size={13} className="shrink-0 text-secondary" />
          <span className="selectable min-w-0 truncate font-medium">{c.branch || "detached HEAD"}</span>
          {c.baseRef && (
            <span className="shrink-0 text-tertiary">← {c.baseRef.replace(/^origin\//, "")}</span>
          )}
        </div>
        <div className="mt-1 text-xs text-secondary">
          {c.files.length ? (
            <>
              {plural(c.files.length, "file")} · <span className="text-ok">+{added}</span>{" "}
              <span className="text-bad">−{removed}</span>
              {c.uncommitted > 0 && ` · ${c.uncommitted} uncommitted`}
            </>
          ) : (
            "No changes yet"
          )}
          {" · "}
          {unpushed === undefined
            ? "not pushed"
            : unpushed > 0
              ? `${plural(unpushed, "commit")} to push`
              : "pushed"}
          {!c.live && c.asOf && " · as of the last look"}
        </div>
      </div>
      <ul className="min-h-0 flex-1 overflow-y-auto py-1">
        {c.files.map((f) => {
          const [dir, name] = splitPath(f.path);
          const on = activeDoc === docId("diff", f.path);
          return (
            <li key={f.path}>
              <button
                type="button"
                onClick={() =>
                  open(s.id, { kind: "diff", path: f.path, oldPath: f.oldPath, status: f.status })
                }
                title={f.oldPath ? `${f.oldPath} → ${f.path}` : f.path}
                className={`flex h-6 w-full items-center gap-2 px-3 text-left text-sm ${on ? "bg-selected" : "hover:bg-hover"}`}
              >
                <StatusMark status={f.status} />
                <span className="min-w-0 flex-1 truncate">
                  <span className="text-tertiary">{dir}</span>
                  {name}
                </span>
                {f.uncommitted && (
                  <span className="size-1.5 shrink-0 rounded-full bg-warn" title="Not committed" />
                )}
                {f.binary ? (
                  <span className="shrink-0 text-2xs text-tertiary">binary</span>
                ) : (
                  <span className="shrink-0 font-mono text-[11px]">
                    {f.added > 0 && <span className="text-ok">+{f.added}</span>}{" "}
                    {f.removed > 0 && <span className="text-bad">−{f.removed}</span>}
                  </span>
                )}
              </button>
            </li>
          );
        })}
      </ul>
      {c.live && (
        <div className="flex shrink-0 flex-wrap gap-1.5 border-t border-separator p-2.5">
          {comments > 0 && (
            <Button
              size="sm"
              variant="primary"
              icon={<Send size={11} />}
              onPress={() => {
                sendToAgent(s, reviewMessage(useComments.getState().comments[s.id] ?? []));
                useComments.getState().clear(s.id);
              }}
            >
              Send {plural(comments, "comment")}
            </Button>
          )}
          <Button
            size="sm"
            icon={<GitCommitHorizontal size={11} />}
            isDisabled={c.uncommitted === 0}
            onPress={() => setCommitting(true)}
          >
            Commit…
          </Button>
          <Button
            size="sm"
            icon={<ArrowUpFromLine size={11} />}
            isDisabled={pushing || unpushed === 0}
            onPress={() => void push()}
          >
            {pushing ? "Pushing…" : "Push"}
          </Button>
          {!s.pr && (
            <Button
              size="sm"
              icon={<GitPullRequest size={11} />}
              onPress={() => void composeInto(s, "createPR")}
            >
              Create PR…
            </Button>
          )}
        </div>
      )}
      <CommitSheet open={committing} onOpenChange={setCommitting} session={s} files={c.uncommitted} />
    </div>
  );
}
