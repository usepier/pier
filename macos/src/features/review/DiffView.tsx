import type { DiffLineAnnotation, SelectedLineRange } from "@pierre/diffs";
import { PatchDiff } from "@pierre/diffs/react";
import { useQuery } from "@tanstack/react-query";
import { FileCode, MessageSquarePlus, Send, Trash2 } from "lucide-react";
import { useMemo, useState } from "react";
import { rpc } from "@/api/client";
import type { Session } from "@/api/protocol.gen";
import { type Comment, reviewMessage, useComments } from "@/state/comments";
import { sendToAgent } from "@/state/currentChat";
import { type Doc, useDocs } from "@/state/docs";
import { usePrefs } from "@/state/prefs";
import { toast } from "@/state/toasts";
import { Button } from "@/ui/Button";
import { Empty } from "@/ui/Empty";
import { Spinner } from "@/ui/Spinner";
import { linesFromPatch } from "./patch";

type Note = { comment?: Comment; draft?: { side: Comment["side"]; line: number; endLine?: number } };

function running(s: Session) {
  return ["running", "working", "idle"].includes(s.state) && s.hasKey;
}

const noComments: Comment[] = [];

/**
 * One file's changes against the branch's base. Click the + beside a line
 * (or drag across several) to comment; comments gather until they go to
 * the agent together.
 */
export function DiffView({ session: s, doc, visible }: { session: Session; doc: Doc; visible: boolean }) {
  const live = running(s);
  const diff = useQuery({
    queryKey: ["diff", s.id, doc.path, doc.oldPath, doc.status],
    queryFn: () =>
      rpc.call("git.diff", { sessionId: s.id, path: doc.path, oldPath: doc.oldPath, status: doc.status }),
    refetchInterval: visible && live ? 8000 : false,
  });
  const split = usePrefs((p) => p.diffSplit);
  const setPrefs = usePrefs((p) => p.set);
  const all = useComments((c) => c.comments[s.id] ?? noComments);
  const mine = useMemo(() => all.filter((c) => c.path === doc.path), [all, doc.path]);
  const [draft, setDraft] = useState<Note["draft"]>();
  const open = useDocs((d) => d.open);
  const patch = diff.data?.patch ?? "";

  const annotations = useMemo<DiffLineAnnotation<Note>[]>(() => {
    const out: DiffLineAnnotation<Note>[] = mine.map((c) => ({
      side: c.side,
      lineNumber: c.endLine ?? c.line,
      metadata: { comment: c },
    }));
    if (draft) out.push({ side: draft.side, lineNumber: draft.endLine ?? draft.line, metadata: { draft } });
    return out;
  }, [mine, draft]);

  const startComment = (r: SelectedLineRange) => {
    const side = r.side ?? "additions";
    const [a, b] = r.start <= r.end ? [r.start, r.end] : [r.end, r.start];
    setDraft({ side, line: a, endLine: b !== a ? b : undefined });
  };

  const sendAll = () => {
    if (!all.length) return;
    sendToAgent(s, reviewMessage(all));
    useComments.getState().clear(s.id);
    toast(`Sent ${all.length} ${all.length === 1 ? "comment" : "comments"} to the agent`, "ok");
  };

  if (diff.error) return <Empty title={doc.path} detail={(diff.error as Error).message} />;
  if (!diff.data) {
    return (
      <div className="flex h-full items-center justify-center">
        <Spinner />
      </div>
    );
  }
  return (
    <div className="flex h-full flex-col">
      <div className="flex h-8 shrink-0 items-center gap-2 border-b border-separator px-3 text-xs text-secondary">
        <span className="min-w-0 flex-1 truncate font-mono">
          {doc.oldPath ? `${doc.oldPath} → ` : ""}
          {doc.path}
        </span>
        {!diff.data.live && diff.data.asOf && <span className="text-tertiary">as of the last look</span>}
        <fieldset
          aria-label="Layout"
          className="m-0 flex h-[22px] min-w-0 rounded-ctl border-0 bg-selected p-0.5"
        >
          {(
            [
              [false, "Unified"],
              [true, "Split"],
            ] as const
          ).map(([on, title]) => (
            <button
              key={title}
              type="button"
              aria-pressed={split === on}
              onClick={() => setPrefs({ diffSplit: on })}
              className={`rounded-[4px] px-2 ${split === on ? "bg-control text-label shadow-sm" : ""}`}
            >
              {title}
            </button>
          ))}
        </fieldset>
        {live && doc.status !== "deleted" && (
          <Button
            size="sm"
            icon={<FileCode size={11} />}
            onPress={() => open(s.id, { kind: "file", path: doc.path })}
          >
            Edit
          </Button>
        )}
        {all.length > 0 && (
          <Button size="sm" variant="primary" icon={<Send size={11} />} onPress={sendAll}>
            Send {all.length} to agent
          </Button>
        )}
      </div>
      <div className="selectable min-h-0 flex-1 overflow-auto">
        {patch.trim() === "" ? (
          <Empty title="No changes" detail="The file matches the base now." />
        ) : (
          <PatchDiff<Note, undefined>
            patch={patch}
            disableWorkerPool
            lineAnnotations={annotations}
            options={{
              theme: { dark: "pierre-dark", light: "pierre-light" },
              themeType: "system",
              diffStyle: split ? "split" : "unified",
              lineDiffType: "word",
              hunkSeparators: "line-info",
              overflow: "wrap",
              disableFileHeader: true,
              enableGutterUtility: live,
              enableLineSelection: live,
              onGutterUtilityClick: startComment,
            }}
            renderAnnotation={(a) =>
              a.metadata?.draft ? (
                <CommentEditor
                  onCancel={() => setDraft(undefined)}
                  onSave={(text) => {
                    const d = a.metadata!.draft!;
                    useComments.getState().add(s.id, {
                      id: crypto.randomUUID(),
                      path: doc.path,
                      side: d.side,
                      line: d.line,
                      endLine: d.endLine,
                      text,
                      code: linesFromPatch(patch, d.side, d.line, d.endLine ?? d.line),
                    });
                    setDraft(undefined);
                  }}
                />
              ) : a.metadata?.comment ? (
                <CommentNote sessionId={s.id} comment={a.metadata.comment} />
              ) : null
            }
          />
        )}
        {diff.data.truncated && (
          <div className="px-4 py-2 text-xs text-tertiary">This diff is long; the rest is in the file.</div>
        )}
      </div>
    </div>
  );
}

function CommentEditor({ onSave, onCancel }: { onSave: (text: string) => void; onCancel: () => void }) {
  const [text, setText] = useState("");
  return (
    <div className="m-2 rounded-card border border-separator bg-control p-2 font-sans shadow-sm">
      <textarea
        autoFocus
        value={text}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Escape") onCancel();
          if (e.key === "Enter" && (e.metaKey || e.ctrlKey) && text.trim()) onSave(text);
        }}
        placeholder="What should change here?"
        rows={2}
        className="selectable block w-full resize-none bg-transparent text-base outline-none placeholder:text-tertiary"
      />
      <div className="mt-1.5 flex justify-end gap-2">
        <Button size="sm" onPress={onCancel}>
          Cancel
        </Button>
        <Button
          size="sm"
          variant="primary"
          icon={<MessageSquarePlus size={11} />}
          isDisabled={!text.trim()}
          onPress={() => onSave(text)}
        >
          Comment
        </Button>
      </div>
    </div>
  );
}

function CommentNote({ sessionId, comment: c }: { sessionId: string; comment: Comment }) {
  return (
    <div className="m-2 flex items-start gap-2 rounded-card border border-accent/30 bg-accent-soft px-3 py-2 font-sans text-base">
      <span className="selectable min-w-0 flex-1 whitespace-pre-wrap">{c.text}</span>
      <button
        type="button"
        aria-label="Delete comment"
        onClick={() => useComments.getState().remove(sessionId, c.id)}
        className="shrink-0 text-tertiary hover:text-bad"
      >
        <Trash2 size={12} />
      </button>
    </div>
  );
}
