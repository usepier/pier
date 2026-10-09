import { ArrowUpRight, MessageSquare, RotateCw, SquareTerminal, Trash2, X } from "lucide-react";
import { useEffect, useMemo } from "react";
import { useShallow } from "zustand/react/shallow";
import { rpc } from "@/api/client";
import type { Attachment, ChatInfo, QueuedPrompt, Session } from "@/api/protocol.gen";
import { running } from "@/lib/session";
import { seenKey, useSeen } from "@/state/attention";
import { useFocused } from "@/state/background";
import { useChat } from "@/state/chat";
import { useJob } from "@/state/jobs";
import { type NewChat, useNewChats } from "@/state/newChats";
import {
  discardPrompt,
  type Outgoing,
  retryPrompt,
  sendPrompt,
  sentGraceMs,
  settleOutbox,
  useOutbox,
} from "@/state/outbox";
import { attempt } from "@/state/toasts";
import { Banner } from "@/ui/Banner";
import { Button, IconButton } from "@/ui/Button";
import { Spinner } from "@/ui/Spinner";
import { Tip } from "@/ui/Tooltip";
import { PendingCard } from "./Cards";
import { Composer, interruptChat, type RunSettings } from "./Composer";
import { Transcript } from "./Transcript";

const agentNames: Record<string, string> = { claude: "Claude", codex: "Codex" };

export function agentName(kind?: string): string {
  return (kind && agentNames[kind]) || "The agent";
}

function clock(iso?: string) {
  if (!iso) return "";
  const d = new Date(iso);
  const today = new Date().toDateString() === d.toDateString();
  return today
    ? d.toLocaleTimeString([], { hour: "numeric", minute: "2-digit" })
    : d.toLocaleString([], { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" });
}

/**
 * One chat of a session: the conversation, what the agent asks, and a
 * composer with the chat's model, effort and permission pickers. A parked
 * session shows what this Mac last saw, and sending to it wakes it. A chat
 * not started yet (draft) starts with its first message.
 */
export function ChatView({
  session: s,
  chatId,
  draft,
  visible = true,
  onTakeover,
}: {
  session: Session;
  chatId: string;
  /** Set while the chat is only opened here, not started. */
  draft?: NewChat;
  visible?: boolean;
  /** Continues the chat in a terminal tab. */
  onTakeover?: () => void;
}) {
  const chat = useChat(s.id, chatId);
  const info: ChatInfo | undefined =
    chat.chat ?? (s.chats?.find((c) => c.id === chatId) as ChatInfo | undefined);
  const agent = info?.agent ?? draft?.agent ?? "claude";
  const name = agentName(agent);
  const live = chat.live && running(s);
  const working = live && info?.state === "working";
  const focused = useFocused();
  const markSeen = useSeen((x) => x.markSeen);
  const updateDraft = useNewChats((n) => n.update);

  // Looking at the chat is seeing how its turn ended.
  useEffect(() => {
    if (visible && focused && info?.since) markSeen(seenKey(s.id, chatId), info.since);
  }, [visible, focused, info?.since, s.id, chatId, markSeen]);

  const outgoing = useOutbox(useShallow((o) => Object.values(o.items).filter((x) => x.chatId === chatId)));
  const queue = info?.queue ?? [];
  const known = useMemo(() => {
    const ids = new Set(queue.map((q) => q.id));
    for (const i of chat.items) if (i.kind === "user") ids.add(i.id);
    return ids;
  }, [queue, chat.items]);
  useEffect(() => {
    settleOutbox(chatId, known);
    const t = setInterval(() => settleOutbox(chatId, known), sentGraceMs / 4);
    return () => clearInterval(t);
  }, [chatId, known]);
  const shown = outgoing.filter((o) => !known.has(o.id)).sort((a, b) => a.at - b.at);

  const settings: RunSettings = {
    model: info?.model ?? draft?.model ?? "",
    effort: info?.effort ?? draft?.effort ?? "",
    mode: info?.mode ?? draft?.mode ?? "",
  };
  const onSettings = (p: Partial<RunSettings>) => {
    if (draft && !info) {
      updateDraft(s.id, chatId, p);
      return;
    }
    void attempt(() => rpc.call("chat.configure", { sessionId: s.id, chatId, ...p }));
  };
  const onSend = (text: string, attachments: Attachment[], mode: "steer" | "queue") => {
    const create =
      draft && !info
        ? { agent, model: settings.model, effort: settings.effort, mode: settings.mode }
        : undefined;
    sendPrompt({ sessionId: s.id, chatId, text, attachments, mode, create });
  };

  const terminal = info?.state === "terminal";
  const failed = info?.state === "error";
  const canSend = s.hasKey && !["creating", "failed", "deleting"].includes(s.state);
  const placeholder =
    s.state === "parked"
      ? `Message ${name} — sending wakes the session`
      : terminal
        ? `${name} goes on in a terminal — messages wait for it`
        : working
          ? `Message ${name}`
          : info || draft
            ? `Message ${name}`
            : "";

  return (
    <div className="flex h-full flex-col bg-surface">
      {!live && <OfflineStrip session={s} asOf={chat.asOf} loaded={chat.loaded} />}
      {terminal && (
        <Banner
          actions={
            onTakeover && (
              <Button size="sm" variant="plain" icon={<SquareTerminal size={13} />} onPress={onTakeover}>
                Show terminal
              </Button>
            )
          }
        >
          This chat goes on in a terminal. It picks up what happens there when that program exits.
        </Banner>
      )}
      {failed && (
        <Banner tone="bad">
          {name} keeps failing to start: {info?.error || "no reason given"}. Sending a message tries again.
        </Banner>
      )}
      <Transcript
        items={chat.items}
        streaming={chat.streaming}
        more={chat.more}
        loadingOlder={chat.loadingOlder}
        onLoadOlder={chat.loadOlder}
        empty={
          chat.loaded || draft ? (
            <div className="flex flex-col items-center gap-2 py-24 text-center text-secondary">
              <MessageSquare size={26} strokeWidth={1.5} className="text-quaternary" />
              <div className="text-base">
                {draft && !info ? `New chat with ${name}` : "Nothing here yet."}
              </div>
              <div className="text-sm text-tertiary">Tell {name} what to do.</div>
            </div>
          ) : (
            <div className="flex justify-center py-24">
              <Spinner />
            </div>
          )
        }
        footer={
          <>
            {queue.map((q) => (
              <QueuedBubble key={q.id} sessionId={s.id} chatId={chatId} q={q} live={live} />
            ))}
            {shown.map((o) => (
              <OutgoingBubble key={o.id} o={o} />
            ))}
            {working && <WorkingLine since={info?.since} />}
          </>
        }
      />
      {live && info?.pending && (
        <div className="shrink-0 px-6">
          <PendingCard sessionId={s.id} chatId={chatId} agent={agent} agentName={name} req={info.pending} />
        </div>
      )}
      <Composer
        sessionId={s.id}
        draftKey={`${s.id}/${chatId}`}
        agent={agent}
        live={live}
        working={!!working}
        canSend={canSend}
        placeholder={placeholder}
        settings={settings}
        onSettings={onSettings}
        onSend={onSend}
        onInterrupt={working || info?.state === "waiting" ? () => interruptChat(s.id, chatId) : undefined}
      />
    </div>
  );
}

function WorkingLine({ since }: { since?: string }) {
  return (
    <div className="flex items-center gap-2 py-1 text-sm">
      <span className="size-1.5 animate-pulse-soft rounded-full bg-accent" />
      <span className="shimmer font-medium">Working</span>
      {since && <Elapsed since={since} />}
    </div>
  );
}

function Elapsed({ since }: { since: string }) {
  const s = Math.max(0, Math.round((Date.now() - new Date(since).getTime()) / 1000));
  return (
    <span className="text-tertiary tabular-nums">
      {s < 60 ? `${s}s` : `${Math.floor(s / 60)}m ${s % 60}s`}
    </span>
  );
}

function OfflineStrip({ session: s, asOf, loaded }: { session: Session; asOf?: string; loaded: boolean }) {
  if (s.state === "parked") return <Banner>Parked{asOf ? ` — the chat as of ${clock(asOf)}` : ""}</Banner>;
  if (!loaded || running(s)) {
    return (
      <div className="flex h-7 shrink-0 items-center justify-center gap-1.5 border-b border-separator text-sm text-secondary">
        <Spinner size={10} /> connecting{asOf ? ` — as of ${clock(asOf)}` : "…"}
      </div>
    );
  }
  return null;
}

function Bubble({
  text,
  note,
  actions,
  tone = "",
}: {
  text: string;
  note: React.ReactNode;
  actions?: React.ReactNode;
  tone?: string;
}) {
  return (
    <div className="group flex flex-col items-end gap-1">
      <div className="selectable max-w-[85%] rounded-[12px] border border-dashed border-strong px-3.5 py-2 text-[14px] whitespace-pre-wrap break-words text-secondary">
        {text}
      </div>
      <div className={`flex items-center gap-2 text-xs ${tone || "text-tertiary"}`}>
        {note}
        {actions}
      </div>
    </div>
  );
}

/** A prompt the session holds until the turn ends. */
function QueuedBubble({
  sessionId,
  chatId,
  q,
  live,
}: {
  sessionId: string;
  chatId: string;
  q: QueuedPrompt;
  live: boolean;
}) {
  return (
    <Bubble
      text={q.text}
      note="queued — goes in when this turn ends"
      actions={
        live && (
          <span className="flex items-center gap-0.5 opacity-0 transition-opacity group-hover:opacity-100">
            <Tip content="Send it into this turn now">
              <IconButton
                label="Send now"
                size="sm"
                onPress={() =>
                  void attempt(async () => {
                    await rpc.call("chat.dequeue", { sessionId, chatId, promptId: q.id });
                    await rpc.call("chat.send", {
                      sessionId,
                      chatId,
                      prompt: { id: `${q.id}-now`, text: q.text, attachments: q.attachments },
                      mode: "steer",
                    });
                  })
                }
              >
                <ArrowUpRight size={12} />
              </IconButton>
            </Tip>
            <Tip content="Take it back">
              <IconButton
                label="Remove from the queue"
                size="sm"
                onPress={() =>
                  void attempt(() => rpc.call("chat.dequeue", { sessionId, chatId, promptId: q.id }))
                }
              >
                <X size={12} />
              </IconButton>
            </Tip>
          </span>
        )
      }
    />
  );
}

function OutgoingBubble({ o }: { o: Outgoing }) {
  if (o.state === "waking") return <WakingBubble o={o} />;
  if (o.state === "failed") {
    return (
      <Bubble
        text={o.text}
        tone="text-bad"
        note={<>couldn't send{o.error ? `: ${o.error}` : ""}</>}
        actions={
          <>
            <button
              type="button"
              onClick={() => retryPrompt(o.id)}
              className="inline-flex items-center gap-0.5 hover:underline"
            >
              <RotateCw size={10} /> retry
            </button>
            <button
              type="button"
              onClick={() => discardPrompt(o.id)}
              className="inline-flex items-center gap-0.5 hover:underline"
            >
              <Trash2 size={10} /> discard
            </button>
          </>
        }
      />
    );
  }
  return (
    <Bubble
      text={o.text}
      note={
        o.state === "sending" ? (
          <>
            <Spinner size={9} /> sending
          </>
        ) : (
          "sent"
        )
      }
    />
  );
}

/** A prompt riding the job that wakes its parked session. */
function WakingBubble({ o }: { o: Outgoing }) {
  const job = useJob(o.jobId);
  const state = job?.job.state;
  useEffect(() => {
    if (state === "done") useOutbox.getState().patch(o.id, { state: "sent", sentAt: Date.now() });
    if (state === "failed" || state === "lost") {
      useOutbox.getState().patch(o.id, { state: "failed", error: job?.job.error || "the wake failed" });
    }
  }, [state, o.id, job?.job.error]);
  const last = job?.events.at(-1)?.message;
  return (
    <Bubble
      text={o.text}
      note={
        <>
          <Spinner size={9} /> {last || "waking the session"}
        </>
      }
    />
  );
}
