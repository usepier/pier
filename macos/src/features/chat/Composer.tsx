import { useQuery } from "@tanstack/react-query";
import { ArrowUp, FileText, Image, Square, X } from "lucide-react";
import { type ClipboardEvent, type DragEvent, type KeyboardEvent, useEffect, useRef, useState } from "react";
import { rpc } from "@/api/client";
import { useDrafts } from "@/state/drafts";
import { sendPrompt } from "@/state/outbox";
import { attempt, toast } from "@/state/toasts";
import { IconButton } from "@/ui/Button";
import { Spinner } from "@/ui/Spinner";
import { insertMention, mentionAt, rank } from "./mentions";

interface Attachment {
  key: string;
  name: string;
  path?: string; // where it landed in the session, once uploaded
  failed?: boolean;
}

const maxImageBytes = 20 << 20;

function base64(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const r = new FileReader();
    r.onload = () => resolve(String(r.result).replace(/^data:[^,]*,/, ""));
    r.onerror = () => reject(r.error);
    r.readAsDataURL(file);
  });
}

/**
 * Where you talk to the agent. Return sends (Shift-Return for a new line);
 * while it works, a message waits for it to finish, and Option-Return
 * sends it right away. Escape stops the agent's turn. @ picks a file; a
 * pasted or dropped image goes into the session and the message names it.
 */
export function Composer({
  sessionId,
  live,
  working,
  canSend,
  placeholder,
}: {
  sessionId: string;
  live: boolean;
  working: boolean;
  canSend: boolean;
  placeholder: string;
}) {
  const text = useDrafts((d) => d.drafts[sessionId] ?? "");
  const setDraft = useDrafts((d) => d.setDraft);
  const area = useRef<HTMLTextAreaElement>(null);
  const [caret, setCaret] = useState(0);
  const [pick, setPick] = useState(0);
  const [dismissed, setDismissed] = useState<number | null>(null);
  const [attachments, setAttachments] = useState<Attachment[]>([]);
  const [dragging, setDragging] = useState(false);

  const files = useQuery({
    queryKey: ["files", sessionId],
    queryFn: () => rpc.call("files.list", { sessionId }),
    enabled: live,
    staleTime: 60_000,
  });
  const at = mentionAt(text, caret);
  const mentionOpen = !!at && dismissed !== at.start && live;
  const matches = mentionOpen ? rank(files.data?.paths ?? [], at.query) : [];

  // Grow with the text, up to a third of the window.
  // biome-ignore lint/correctness/useExhaustiveDependencies: measures again whenever the text changes
  useEffect(() => {
    const el = area.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${Math.min(el.scrollHeight, Math.max(120, window.innerHeight / 3))}px`;
  }, [text]);
  // biome-ignore lint/correctness/useExhaustiveDependencies: a new query starts at the top match
  useEffect(() => setPick(0), [at?.query]);

  const uploading = attachments.some((a) => !a.path && !a.failed);

  const send = (mode?: "now") => {
    const paths = attachments.filter((a) => a.path).map((a) => a.path!);
    const body = [text.trim(), ...paths].filter(Boolean).join("\n\n");
    if (!body || uploading || !canSend) return;
    sendPrompt(sessionId, body, mode);
    setDraft(sessionId, "");
    setAttachments([]);
  };

  const choose = (path: string) => {
    if (!at) return;
    const r = insertMention(text, at, path);
    setDraft(sessionId, r.text);
    requestAnimationFrame(() => {
      area.current?.setSelectionRange(r.caret, r.caret);
      setCaret(r.caret);
    });
  };

  const onKey = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.nativeEvent.isComposing) return;
    if (mentionOpen && matches.length) {
      if (e.key === "ArrowDown" || e.key === "ArrowUp") {
        e.preventDefault();
        setPick((p) => (p + (e.key === "ArrowDown" ? 1 : matches.length - 1)) % matches.length);
        return;
      }
      if (e.key === "Enter" || e.key === "Tab") {
        e.preventDefault();
        choose(matches[pick]!);
        return;
      }
    }
    if (e.key === "Escape") {
      e.preventDefault();
      if (mentionOpen && at) setDismissed(at.start);
      else if (working) void attempt(() => rpc.call("chat.interrupt", { id: sessionId }));
      return;
    }
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      send(e.altKey && working ? "now" : undefined);
    }
  };

  const addImages = (list: File[]) => {
    const images = list.filter((f) => f.type.startsWith("image/"));
    if (!images.length) return false;
    if (!live) {
      toast("Wake the session to attach images", "info");
      return true;
    }
    for (const f of images) {
      const key = crypto.randomUUID();
      const name = f.name || `pasted.${f.type.split("/")[1] ?? "png"}`;
      if (f.size > maxImageBytes) {
        toast(`${name} is over 20 MB`, "bad");
        continue;
      }
      setAttachments((a) => [...a, { key, name }]);
      void (async () => {
        try {
          const r = await rpc.call("files.upload", { sessionId, name, data: await base64(f) });
          setAttachments((a) => a.map((x) => (x.key === key ? { ...x, path: r.path } : x)));
        } catch (e) {
          setAttachments((a) => a.map((x) => (x.key === key ? { ...x, failed: true } : x)));
          toast(`Couldn't attach ${name}: ${(e as Error).message}`, "bad");
        }
      })();
    }
    return true;
  };

  const onPaste = (e: ClipboardEvent) => {
    if (addImages([...e.clipboardData.files])) e.preventDefault();
  };
  const onDrop = (e: DragEvent) => {
    setDragging(false);
    if (addImages([...e.dataTransfer.files])) e.preventDefault();
  };

  const empty = !text.trim() && !attachments.some((a) => a.path);
  return (
    <div className="shrink-0 px-5 pb-4">
      {/* biome-ignore lint/a11y/noStaticElementInteractions: a drop target for images; the textarea is the control */}
      <div
        onDragOver={(e) => {
          if ([...e.dataTransfer.items].some((i) => i.type.startsWith("image/"))) {
            e.preventDefault();
            setDragging(true);
          }
        }}
        onDragLeave={() => setDragging(false)}
        onDrop={onDrop}
        className={`relative mx-auto max-w-3xl rounded-[12px] border bg-control shadow-[0_1px_3px_rgb(0_0_0/0.06)] ${dragging ? "border-accent" : "border-separator"}`}
      >
        {mentionOpen && matches.length > 0 && (
          <div
            role="listbox"
            aria-label="Files"
            className="absolute bottom-full left-2 mb-1 w-[min(480px,90%)] overflow-hidden rounded-card border border-separator bg-control py-1 shadow-lg"
          >
            {matches.map((p, i) => (
              <div
                key={p}
                role="option"
                tabIndex={-1}
                aria-selected={i === pick}
                onMouseDown={(e) => {
                  e.preventDefault();
                  choose(p);
                }}
                className={`flex items-center gap-2 px-2.5 py-1 text-sm ${i === pick ? "bg-accent text-on-accent" : ""}`}
              >
                <FileText size={12} className="shrink-0 opacity-70" />
                <span className="truncate font-mono text-[11.5px]">{p}</span>
              </div>
            ))}
          </div>
        )}
        {attachments.length > 0 && (
          <div className="flex flex-wrap gap-1.5 px-3 pt-2.5">
            {attachments.map((a) => (
              <span
                key={a.key}
                className={`inline-flex h-6 items-center gap-1 rounded-ctl border border-separator px-2 text-xs ${a.failed ? "text-bad" : "text-secondary"}`}
              >
                {a.path || a.failed ? <Image size={12} /> : <Spinner size={11} />}
                <span className="max-w-40 truncate">{a.name}</span>
                <button
                  type="button"
                  aria-label={`Remove ${a.name}`}
                  onClick={() => setAttachments((x) => x.filter((y) => y.key !== a.key))}
                  className="text-tertiary hover:text-label"
                >
                  <X size={11} />
                </button>
              </span>
            ))}
          </div>
        )}
        <textarea
          ref={area}
          rows={1}
          value={text}
          disabled={!canSend}
          placeholder={placeholder}
          aria-label="Message"
          onChange={(e) => {
            setDraft(sessionId, e.target.value);
            setCaret(e.target.selectionStart);
          }}
          onSelect={(e) => setCaret(e.currentTarget.selectionStart)}
          onKeyDown={onKey}
          onPaste={onPaste}
          className="selectable block max-h-[50vh] w-full resize-none bg-transparent px-3.5 pt-2.5 pb-1 text-base leading-[1.45] outline-none placeholder:text-tertiary disabled:opacity-50"
        />
        <div className="flex items-center gap-1 px-2 pb-2">
          <span className="flex-1 truncate px-1.5 text-2xs text-tertiary">
            {working ? "⏎ queues · ⌥⏎ sends now · esc stops" : live ? "@ for files · paste images" : ""}
          </span>
          {working && (
            <IconButton
              label="Stop the agent"
              className="size-7"
              onPress={() => void attempt(() => rpc.call("chat.interrupt", { id: sessionId }))}
            >
              <Square size={12} fill="currentColor" />
            </IconButton>
          )}
          <IconButton
            label="Send"
            isDisabled={empty || uploading || !canSend}
            onPress={() => send()}
            className="size-7 bg-accent text-on-accent hover:bg-accent hover:text-on-accent hover:brightness-105 disabled:bg-selected disabled:text-tertiary"
          >
            <ArrowUp size={15} strokeWidth={2.4} />
          </IconButton>
        </div>
      </div>
    </div>
  );
}
