import { useQuery } from "@tanstack/react-query";
import { ArrowUp, FileText, Gauge, Paperclip, ShieldCheck, Square, X } from "lucide-react";
import { type ClipboardEvent, type DragEvent, type KeyboardEvent, useEffect, useRef, useState } from "react";
import { rpc } from "@/api/client";
import type { AgentModels, Attachment } from "@/api/protocol.gen";
import { useDrafts } from "@/state/drafts";
import { usePrefs } from "@/state/prefs";
import { attempt, toast } from "@/state/toasts";
import { IconButton } from "@/ui/Button";
import { type Option, Select } from "@/ui/Select";
import { Spinner } from "@/ui/Spinner";
import { Tip } from "@/ui/Tooltip";
import { insertMention, mentionAt, rank } from "./mentions";

/** A chat's settings, as the composer's pickers show and change them. */
export interface RunSettings {
  model: string;
  effort: string;
  mode: string;
}

interface Pending {
  key: string;
  name: string;
  attachment?: Attachment;
  failed?: boolean;
}

const maxFileBytes = 20 << 20;

function base64(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const r = new FileReader();
    r.onload = () => resolve(String(r.result).replace(/^data:[^,]*,/, ""));
    r.onerror = () => reject(r.error);
    r.readAsDataURL(file);
  });
}

/** What an agent offers, for the pickers. */
export function useModels(agent: string, sessionId?: string) {
  return useQuery({
    queryKey: ["models", agent, sessionId ?? ""],
    queryFn: () => rpc.call("agents.models", { agent, sessionId }),
    staleTime: 5 * 60_000,
  });
}

const effortLabels: Record<string, string> = {
  minimal: "Minimal",
  low: "Low",
  medium: "Medium",
  high: "High",
  xhigh: "Extra high",
  max: "Max",
};

/** The model, effort and permission pickers. */
export function RunPickers({
  models,
  value,
  onChange,
  disabled,
}: {
  models?: AgentModels;
  value: RunSettings;
  onChange: (p: Partial<RunSettings>) => void;
  disabled?: boolean;
}) {
  const list = models?.models ?? [];
  const current = list.find((m) => m.id === value.model) ?? list.find((m) => m.default);
  const modelOptions: Option[] = list.map((m) => ({
    value: m.id,
    label: m.name || m.id || "Default",
    description: m.description,
  }));
  if (value.model && !list.some((m) => m.id === value.model))
    modelOptions.unshift({ value: value.model, label: value.model });
  const efforts = current?.efforts ?? [];
  const effortOptions: Option[] = [
    {
      value: "",
      label: current?.defaultEffort
        ? `Default (${effortLabels[current.defaultEffort] ?? current.defaultEffort})`
        : "Default",
    },
    ...efforts.map((e) => ({ value: e, label: effortLabels[e] ?? e })),
  ];
  const modeOptions: Option[] = (models?.modes ?? []).map((m) => ({
    value: m.id,
    label: m.name,
    description: m.description,
  }));
  return (
    <>
      {modelOptions.length > 0 && (
        <Select
          label="Model"
          variant="ghost"
          size="sm"
          placement="top start"
          isDisabled={disabled}
          value={value.model || current?.id || ""}
          onChange={(model) => onChange({ model })}
          options={modelOptions}
        />
      )}
      {efforts.length > 0 && (
        <Tip content="Effort: how long it thinks">
          <div className="inline-flex items-center">
            <Select
              label="Effort"
              variant="ghost"
              size="sm"
              placement="top start"
              isDisabled={disabled}
              value={value.effort}
              onChange={(effort) => onChange({ effort })}
              options={effortOptions.map((o) => ({ ...o, icon: <Gauge size={12} /> }))}
            />
          </div>
        </Tip>
      )}
      {modeOptions.length > 0 && (
        <Select
          label="Permissions"
          variant="ghost"
          size="sm"
          placement="top start"
          isDisabled={disabled}
          value={value.mode || "default"}
          onChange={(mode) => onChange({ mode })}
          options={modeOptions.map((o) => ({ ...o, icon: <ShieldCheck size={12} /> }))}
        />
      )}
    </>
  );
}

/**
 * Where you talk to a chat's agent. Return sends (or ⌘Return, as set); a
 * message sent mid-turn steers the turn or waits for it to end, as set,
 * and ⌘⇧Return does the other. Escape stops the turn. @ picks a file;
 * files of any kind can be pasted, dropped or attached — a parked
 * session's wait on this Mac until the message wakes it.
 */
export function Composer({
  sessionId,
  draftKey,
  agent,
  live,
  working,
  canSend,
  placeholder,
  settings,
  onSettings,
  onSend,
  onInterrupt,
}: {
  sessionId: string;
  /** Where the unsent text is kept: one draft per chat. */
  draftKey: string;
  agent: string;
  live: boolean;
  working: boolean;
  canSend: boolean;
  placeholder: string;
  settings: RunSettings;
  onSettings: (p: Partial<RunSettings>) => void;
  onSend: (text: string, attachments: Attachment[], mode: "steer" | "queue") => void;
  onInterrupt?: () => void;
}) {
  const text = useDrafts((d) => d.drafts[draftKey] ?? "");
  const setDraft = useDrafts((d) => d.setDraft);
  const sendKey = usePrefs((p) => p.sendKey);
  const followUp = usePrefs((p) => p.followUp);
  const area = useRef<HTMLTextAreaElement>(null);
  const picker = useRef<HTMLInputElement>(null);
  const [caret, setCaret] = useState(0);
  const [pick, setPick] = useState(0);
  const [dismissed, setDismissed] = useState<number | null>(null);
  const [pending, setPending] = useState<Pending[]>([]);
  const [dragging, setDragging] = useState(false);
  const models = useModels(agent, live ? sessionId : undefined);

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

  const uploading = pending.some((a) => !a.attachment && !a.failed);

  const send = (other: boolean) => {
    const attachments = pending.filter((a) => a.attachment).map((a) => a.attachment!);
    const body = text.trim();
    if ((!body && !attachments.length) || uploading || !canSend) return;
    const mode = working ? (other ? (followUp === "steer" ? "queue" : "steer") : followUp) : "queue";
    onSend(body, attachments, mode);
    setDraft(draftKey, "");
    setPending([]);
  };

  const choose = (path: string) => {
    if (!at) return;
    const r = insertMention(text, at, path);
    setDraft(draftKey, r.text);
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
      else if (working) onInterrupt?.();
      return;
    }
    if (e.key !== "Enter") return;
    const sends = sendKey === "enter" ? !e.shiftKey : e.metaKey || e.ctrlKey;
    if (sends || (e.metaKey && e.shiftKey)) {
      e.preventDefault();
      send(e.shiftKey && (e.metaKey || e.ctrlKey));
    }
  };

  const addFiles = (list: File[]) => {
    if (!list.length) return false;
    for (const f of list) {
      const key = crypto.randomUUID();
      const name = f.name || `pasted.${f.type.split("/")[1] ?? "bin"}`;
      if (f.size > maxFileBytes) {
        toast(`${name} is over 20 MB`, "bad");
        continue;
      }
      setPending((a) => [...a, { key, name }]);
      void (async () => {
        try {
          const r = await rpc.call("files.upload", {
            sessionId,
            name,
            mime: f.type || undefined,
            data: await base64(f),
          });
          setPending((a) => a.map((x) => (x.key === key ? { ...x, attachment: r } : x)));
        } catch (e) {
          setPending((a) => a.map((x) => (x.key === key ? { ...x, failed: true } : x)));
          toast(`Couldn't attach ${name}: ${(e as Error).message}`, "bad");
        }
      })();
    }
    return true;
  };

  const onPaste = (e: ClipboardEvent) => {
    if (addFiles([...e.clipboardData.files])) e.preventDefault();
  };
  const onDrop = (e: DragEvent) => {
    setDragging(false);
    if (addFiles([...e.dataTransfer.files])) e.preventDefault();
  };

  const empty = !text.trim() && !pending.some((a) => a.attachment);
  const hint = working
    ? `${followUp === "steer" ? "⏎ steers this turn · ⌘⇧⏎ queues" : "⏎ queues · ⌘⇧⏎ steers this turn"} · esc stops`
    : sendKey === "cmdEnter"
      ? "⌘⏎ sends"
      : "";
  return (
    <div className="shrink-0 px-6 pb-4">
      {/* biome-ignore lint/a11y/noStaticElementInteractions: a drop target for files; the textarea is the control */}
      <div
        onDragOver={(e) => {
          if ([...e.dataTransfer.items].some((i) => i.kind === "file")) {
            e.preventDefault();
            setDragging(true);
          }
        }}
        onDragLeave={() => setDragging(false)}
        onDrop={onDrop}
        className={`relative mx-auto max-w-3xl rounded-[14px] border bg-control transition-colors ${dragging ? "border-accent" : "border-strong focus-within:border-quaternary"}`}
      >
        {mentionOpen && matches.length > 0 && (
          <div
            role="listbox"
            aria-label="Files"
            className="pop-in absolute bottom-full left-2 mb-1.5 w-[min(480px,90%)] overflow-hidden rounded-card bg-popover p-1 shadow-(--shadow-pop)"
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
                onMouseEnter={() => setPick(i)}
                className={`flex cursor-pointer items-center gap-2 rounded-[6px] px-2 py-1 text-sm ${i === pick ? "bg-hover" : ""}`}
              >
                <FileText size={12} className="shrink-0 text-tertiary" />
                <span className="truncate font-mono text-[12px]">{p}</span>
              </div>
            ))}
          </div>
        )}
        {pending.length > 0 && (
          <div className="flex flex-wrap gap-1.5 px-3 pt-3">
            {pending.map((a) => (
              <span
                key={a.key}
                className={`inline-flex h-7 items-center gap-1.5 rounded-[7px] border border-separator bg-raised pr-1 pl-2 text-sm ${a.failed ? "text-bad" : "text-secondary"}`}
              >
                {a.attachment || a.failed ? <Paperclip size={12} /> : <Spinner size={11} />}
                <span className="max-w-48 truncate">{a.name}</span>
                <IconButton
                  label={`Remove ${a.name}`}
                  size="sm"
                  className="size-5"
                  onPress={() => setPending((x) => x.filter((y) => y.key !== a.key))}
                >
                  <X size={11} />
                </IconButton>
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
            setDraft(draftKey, e.target.value);
            setCaret(e.target.selectionStart);
          }}
          onSelect={(e) => setCaret(e.currentTarget.selectionStart)}
          onKeyDown={onKey}
          onPaste={onPaste}
          className="selectable block max-h-[50vh] w-full resize-none bg-transparent px-4 pt-3 pb-1.5 text-[14px] leading-[1.5] outline-none placeholder:text-tertiary focus-visible:outline-none disabled:opacity-50"
        />
        <div className="flex items-center gap-0.5 px-2 pb-2">
          <RunPickers models={models.data} value={settings} onChange={onSettings} />
          <span className="min-w-0 flex-1 truncate px-2 text-right text-xs text-tertiary">{hint}</span>
          <input
            ref={picker}
            type="file"
            multiple
            hidden
            onChange={(e) => {
              addFiles([...(e.target.files ?? [])]);
              e.target.value = "";
            }}
          />
          <Tip content="Attach files">
            <IconButton label="Attach files" onPress={() => picker.current?.click()} isDisabled={!canSend}>
              <Paperclip size={15} />
            </IconButton>
          </Tip>
          {working && onInterrupt && (
            <Tip content="Stop (esc)">
              <IconButton label="Stop the agent" onPress={onInterrupt} className="text-label">
                <Square size={12} fill="currentColor" />
              </IconButton>
            </Tip>
          )}
          <IconButton
            label="Send"
            isDisabled={empty || uploading || !canSend}
            onPress={() => send(false)}
            className="ml-0.5 rounded-full bg-accent text-on-accent hover:bg-accent hover:text-on-accent hover:brightness-110 disabled:bg-selected disabled:text-tertiary"
          >
            <ArrowUp size={15} strokeWidth={2.4} />
          </IconButton>
        </div>
      </div>
    </div>
  );
}

/** Stops a chat's turn. */
export function interruptChat(sessionId: string, chatId: string) {
  void attempt(() => rpc.call("chat.interrupt", { sessionId, chatId }));
}
