import { Compartment, EditorState } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { FileWarning, MessageSquarePlus, Send, Trash2 } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { rpc } from "@/api/client";
import type { FileContent, Session } from "@/api/protocol.gen";
import { Disconnected, RpcError } from "@/api/rpc";
import { CommentEditor } from "@/features/review/CommentBox";
import { ask } from "@/lib/confirm";
import { bytes } from "@/lib/format";
import { readable } from "@/lib/session";
import { bufferKey, useBuffers } from "@/state/buffers";
import { type Comment, reviewMessage, useComments } from "@/state/comments";
import { sendToAgent } from "@/state/currentChat";
import { type Doc, useDocs } from "@/state/docs";
import { toast, toastError } from "@/state/toasts";
import { Button } from "@/ui/Button";
import { Empty } from "@/ui/Empty";
import { Spinner } from "@/ui/Spinner";
import { editorExtensions, languageFor, markCommented } from "./codemirror";
import { setActiveSave } from "./save";

const noComments: Comment[] = [];

/**
 * A file of the session's checkout in an editor. Edits are kept on this Mac
 * as you type until saved; ⌘S saves them to the session — unless the file
 * changed there since it was opened, which asks first. A parked session
 * shows a changed file as of the last look, read-only.
 */
export function FileEditor({ session: s, doc, visible }: { session: Session; doc: Doc; visible: boolean }) {
  const qc = useQueryClient();
  const live = readable(s);
  const queryKey = useMemo(() => ["file", s.id, doc.path, live], [s.id, doc.path, live]);
  const file = useQuery({
    queryKey,
    queryFn: () => rpc.call("files.read", { sessionId: s.id, path: doc.path }),
    staleTime: Number.POSITIVE_INFINITY,
    retry: false,
  });
  const key = bufferKey(s.id, doc.path);
  const host = useRef<HTMLDivElement>(null);
  const view = useRef<EditorView | null>(null);
  const lang = useRef(new Compartment());
  const [saving, setSaving] = useState(false);
  const dirty = useBuffers((b) => !!b.buffers[key]);
  const reveal = useDocs((d) => d.reveal);
  // The lines selected (or the cursor's), for a comment on them.
  const [sel, setSel] = useState<{ line: number; endLine: number }>();
  const [commenting, setCommenting] = useState<{ line: number; endLine: number }>();
  const all = useComments((c) => c.comments[s.id] ?? noComments);
  const mine = useMemo(
    () => all.filter((c) => c.path === doc.path && c.side === "additions"),
    [all, doc.path],
  );

  const save = useCallback(async () => {
    const v = view.current;
    const f = qc.getQueryData<FileContent>(queryKey);
    if (!v || !f || f.asOf) return;
    const text = v.state.doc.toString();
    const buf = useBuffers.getState().buffers[key];
    let base = buf?.baseSha ?? f.sha;
    const opId = crypto.randomUUID();
    setSaving(true);
    try {
      for (let attempt = 0; ; attempt++) {
        try {
          const r = await rpc.call("files.write", {
            sessionId: s.id,
            opId,
            path: doc.path,
            text,
            baseSha: base,
          });
          qc.setQueryData<FileContent>(queryKey, {
            ...f,
            text,
            sha: r.sha,
            missing: false,
            size: new Blob([text]).size,
          });
          // Typed on while it saved: those edits are still unsaved.
          if (v.state.doc.toString() === text) useBuffers.getState().drop(s.id, doc.path);
          else useBuffers.getState().put(s.id, doc.path, { text: v.state.doc.toString(), baseSha: r.sha });
          return;
        } catch (e) {
          // pier restarted mid-save: the same id again is safe.
          if (e instanceof Disconnected && attempt < 3) {
            await new Promise((r) => setTimeout(r, 1500));
            continue;
          }
          if (e instanceof RpcError && e.kind === "conflict") {
            const choice = await ask(
              `${doc.path} changed in the session`,
              "Something in the session (the agent, a terminal) changed the file after you opened it.",
              ["Overwrite with mine", "Keep theirs", "Cancel"],
            );
            if (choice === 0) {
              const cur = await rpc.call("files.read", { sessionId: s.id, path: doc.path });
              base = cur.sha;
              continue;
            }
            if (choice === 1) {
              useBuffers.getState().drop(s.id, doc.path);
              await qc.invalidateQueries({ queryKey });
            }
            return;
          }
          throw e;
        }
      }
    } catch (e) {
      toastError(e);
    } finally {
      setSaving(false);
    }
  }, [qc, s.id, doc.path, key, queryKey]);

  // ⌘S from the menu bar saves the editor in view.
  useEffect(() => {
    if (!visible) return;
    return setActiveSave(() => void save());
  }, [visible, save]);

  // The editor, made once the file has been read — and made again only for
  // another file: a save updates the query, not the editor.
  const data = file.data;
  const shown = !!data && !data.binary && !data.tooLarge;
  const kept = !!data?.asOf;
  const saveRef = useRef(save);
  saveRef.current = save;
  const textRef = useRef(data?.text ?? "");
  textRef.current = data?.text ?? "";
  useEffect(() => {
    if (!host.current || !shown) return;
    const buf = kept ? undefined : useBuffers.getState().buffers[bufferKey(s.id, doc.path)];
    const v = new EditorView({
      parent: host.current,
      state: EditorState.create({
        doc: buf?.text ?? textRef.current,
        extensions: [
          ...editorExtensions(() => void saveRef.current()),
          lang.current.of([]),
          ...(kept ? [EditorState.readOnly.of(true), EditorView.editable.of(false)] : []),
          EditorView.updateListener.of((u) => {
            if (u.selectionSet || u.docChanged) {
              const r = u.state.selection.main;
              const a = u.state.doc.lineAt(r.from).number;
              const b = u.state.doc.lineAt(r.empty ? r.from : r.to - (r.to > r.from ? 1 : 0)).number;
              setSel({ line: Math.min(a, b), endLine: Math.max(a, b) });
            }
            if (!u.docChanged) return;
            const text = u.state.doc.toString();
            const saved = qc.getQueryData<FileContent>(queryKey);
            const store = useBuffers.getState();
            const k = bufferKey(s.id, doc.path);
            if (saved && text === saved.text) store.drop(s.id, doc.path);
            else store.put(s.id, doc.path, { text, baseSha: store.buffers[k]?.baseSha ?? saved?.sha ?? "" });
          }),
        ],
      }),
    });
    view.current = v;
    void languageFor(doc.path).then((ext) => v.dispatch({ effects: lang.current.reconfigure(ext) }));
    return () => {
      v.destroy();
      view.current = null;
    };
  }, [shown, kept, doc.path, s.id, qc, queryKey]);

  // The lines this file's comments are about stand out.
  // biome-ignore lint/correctness/useExhaustiveDependencies: data makes the editor anew
  useEffect(() => {
    const lines = mine.flatMap((c) =>
      Array.from({ length: (c.endLine ?? c.line) - c.line + 1 }, (_, i) => c.line + i),
    );
    view.current?.dispatch({ effects: markCommented.of(lines) });
  }, [mine, data]);

  const addComment = (text: string) => {
    const at = commenting;
    const v = view.current;
    if (!at || !v) return;
    const doc_ = v.state.doc;
    const code = doc_.sliceString(doc_.line(at.line).from, doc_.line(Math.min(at.endLine, doc_.lines)).to);
    useComments.getState().add(s.id, {
      id: crypto.randomUUID(),
      path: doc.path,
      side: "additions",
      line: at.line,
      endLine: at.endLine !== at.line ? at.endLine : undefined,
      text,
      code,
    });
    setCommenting(undefined);
    v.focus();
  };
  const sendAll = () => {
    if (!all.length) return;
    sendToAgent(s, reviewMessage(all));
    useComments.getState().clear(s.id);
    toast(`Sent ${all.length} ${all.length === 1 ? "comment" : "comments"} to the agent`, "ok");
  };

  // Show the line a search hit pointed at — again for each new request, and
  // once the editor exists.
  // biome-ignore lint/correctness/useExhaustiveDependencies: reveal and data are the triggers
  useEffect(() => {
    const v = view.current;
    if (!v || !doc.line || !visible) return;
    const line = v.state.doc.line(Math.min(doc.line, v.state.doc.lines));
    v.dispatch({
      selection: { anchor: line.from },
      effects: EditorView.scrollIntoView(line.from, { y: "center" }),
    });
    v.focus();
  }, [doc.line, reveal, visible, data]);

  if (file.error) {
    const parked = file.error instanceof RpcError && file.error.kind === "parked";
    return (
      <Empty
        icon={<FileWarning size={24} strokeWidth={1.5} />}
        title={doc.path}
        detail={
          parked
            ? "Only changed files are kept while a session is parked. Wake it to open this one."
            : (file.error as Error).message
        }
      />
    );
  }
  if (!data) {
    return (
      <div className="flex h-full items-center justify-center">
        <Spinner />
      </div>
    );
  }
  if (data.binary || data.tooLarge) {
    return (
      <Empty
        icon={<FileWarning size={24} strokeWidth={1.5} />}
        title={doc.path}
        detail={
          data.binary
            ? `A binary file (${bytes(data.size)}).`
            : `Too large to edit here (${bytes(data.size)}).`
        }
      />
    );
  }
  return (
    <div className="flex h-full flex-col">
      <div className="flex h-8 shrink-0 items-center gap-2 border-b border-separator px-3 text-xs text-secondary">
        <span className="min-w-0 flex-1 truncate font-mono">{doc.path}</span>
        {data.missing && <span className="text-tertiary">new file</span>}
        <Button
          size="sm"
          variant="plain"
          icon={<MessageSquarePlus size={11} />}
          isDisabled={!sel}
          onPress={() => setCommenting(sel)}
        >
          Comment
        </Button>
        {all.length > 0 && (
          <Button size="sm" variant="primary" icon={<Send size={11} />} onPress={sendAll}>
            Send {all.length} to agent
          </Button>
        )}
        {kept ? (
          <span className="text-tertiary">as of the last look · read-only</span>
        ) : (
          <>
            {dirty && <span className="text-label">edited</span>}
            <Button
              size="sm"
              variant={dirty ? "primary" : "secondary"}
              isDisabled={!dirty || saving}
              onPress={() => void save()}
            >
              {saving ? "Saving…" : "Save"}
            </Button>
          </>
        )}
      </div>
      {commenting && (
        <CommentEditor
          title={
            commenting.endLine !== commenting.line
              ? `Lines ${commenting.line}–${commenting.endLine}`
              : `Line ${commenting.line}`
          }
          onSave={addComment}
          onCancel={() => setCommenting(undefined)}
        />
      )}
      {mine.length > 0 && (
        <ul className="max-h-32 shrink-0 overflow-y-auto border-b border-separator">
          {mine.map((c) => (
            <li key={c.id} className="flex items-start gap-2 px-3 py-1 text-xs">
              <button
                type="button"
                className="shrink-0 font-mono text-tertiary hover:text-label"
                onClick={() => {
                  const v = view.current;
                  if (!v) return;
                  const at = v.state.doc.line(Math.min(c.line, v.state.doc.lines)).from;
                  v.dispatch({
                    selection: { anchor: at },
                    effects: EditorView.scrollIntoView(at, { y: "center" }),
                  });
                  v.focus();
                }}
              >
                {c.endLine ? `${c.line}–${c.endLine}` : c.line}
              </button>
              <span className="selectable min-w-0 flex-1 whitespace-pre-wrap text-secondary">{c.text}</span>
              <button
                type="button"
                aria-label="Delete comment"
                onClick={() => useComments.getState().remove(s.id, c.id)}
                className="shrink-0 text-tertiary hover:text-bad"
              >
                <Trash2 size={11} />
              </button>
            </li>
          ))}
        </ul>
      )}
      <div ref={host} className="selectable min-h-0 flex-1 overflow-hidden" />
    </div>
  );
}

/** Says why an editor tab can't close yet: unsaved edits. Resolves true to close. */
export async function confirmClose(sessionId: string, path: string): Promise<boolean> {
  if (!useBuffers.getState().buffers[bufferKey(sessionId, path)]) return true;
  const choice = await ask(`Close ${path}?`, "Your edits aren't saved to the session.", [
    "Discard edits",
    "Keep editing",
  ]);
  if (choice !== 0) return false;
  useBuffers.getState().drop(sessionId, path);
  toast(`Discarded your edits to ${path}`, "info");
  return true;
}
