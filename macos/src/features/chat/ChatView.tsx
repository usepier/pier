import { MessageSquare, RotateCw, Trash2 } from "lucide-react";
import { useEffect, useMemo } from "react";
import { useShallow } from "zustand/react/shallow";
import type { AgentDetail, QueuedPrompt, Session } from "@/api/protocol.gen";
import { useSeen } from "@/state/attention";
import { useFocused } from "@/state/background";
import { useChat } from "@/state/chat";
import { useJob } from "@/state/jobs";
import {
  discardPrompt,
  type Outgoing,
  retryPrompt,
  sentGraceMs,
  settleOutbox,
  useOutbox,
} from "@/state/outbox";
import { Banner } from "@/ui/Banner";
import { Spinner } from "@/ui/Spinner";
import { PermissionCard, TerminalWaitCard } from "./Cards";
import { Composer } from "./Composer";
import { Transcript } from "./Transcript";
import { Working } from "./Working";

const agentNames: Record<string, string> = { claude: "Claude", codex: "Codex" };

export function agentName(kind?: string): string {
  return (kind && agentNames[kind]) || "The agent";
}

function running(s: Session) {
  return ["running", "working", "idle"].includes(s.state);
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
 * A session's agent conversation: what the agent wrote, read from its own
 * transcript, and a composer to talk to it. A parked session shows what
 * this Mac last saw, and sending to it wakes it.
 */
export function ChatView({
  session: s,
  visible = true,
  onTerminal,
}: {
  session: Session;
  visible?: boolean;
  /** Shows the agent's own terminal (for what only it can answer). */
  onTerminal?: () => void;
}) {
  const chat = useChat(s.id);
  const live = chat.live && running(s);
  const agent: AgentDetail | undefined = chat.agent ?? (s.agent ? { ...s.agent, queue: [] } : undefined);
  const working = live && agent?.state === "working";
  const name = agentName(agent?.kind);
  const focused = useFocused();
  const markSeen = useSeen((x) => x.markSeen);

  // Looking at the chat is seeing how the turn ended.
  useEffect(() => {
    if (visible && focused && agent?.since) markSeen(s.id, agent.since);
  }, [visible, focused, agent?.since, s.id, markSeen]);

  const outgoing = useOutbox(useShallow((o) => Object.values(o.items).filter((x) => x.sessionId === s.id)));
  const queue = agent?.queue ?? [];
  const queueKey = queue.map((q) => q.id).join(",");
  useEffect(() => {
    settleOutbox(s.id, new Set(queueKey ? queueKey.split(",") : []));
    const t = setInterval(
      () => settleOutbox(s.id, new Set(queueKey ? queueKey.split(",") : [])),
      sentGraceMs / 4,
    );
    return () => clearInterval(t);
  }, [s.id, queueKey]);

  const pending = useMemo(() => {
    const inQueue = new Set(queue.map((q) => q.id));
    return { outgoing: outgoing.filter((o) => !inQueue.has(o.id)).sort((a, b) => a.at - b.at), queue };
  }, [outgoing, queue]);

  const canSend = !!agent && (agent.state !== "ended" || s.state === "parked");
  const placeholder = !agent
    ? "No agent runs in this session"
    : s.state === "parked"
      ? `Message ${name} — sending wakes the session`
      : agent.state === "ended"
        ? `${name} has exited — start it again in its terminal`
        : working
          ? `Message ${name} — it reads it when this turn is done`
          : `Message ${name}`;

  return (
    <div className="flex h-full flex-col">
      {!live && <OfflineStrip session={s} asOf={chat.asOf} loaded={chat.loaded} />}
      {chat.partial && (
        <Banner>
          Part of this conversation couldn't be read here — the agent's terminal shows all of it.
        </Banner>
      )}
      <Transcript
        items={chat.items}
        more={chat.more}
        loadingOlder={chat.loadingOlder}
        onLoadOlder={chat.loadOlder}
        empty={
          chat.loaded ? (
            <div className="flex flex-col items-center gap-2 py-16 text-center text-secondary">
              <MessageSquare size={24} strokeWidth={1.5} className="text-tertiary" />
              <div className="text-sm">
                {agent ? `Nothing yet. Tell ${name} what to do.` : "No conversation yet."}
              </div>
            </div>
          ) : (
            <div className="flex justify-center py-16">
              <Spinner />
            </div>
          )
        }
        footer={
          <>
            {pending.queue.map((q) => (
              <QueuedBubble key={q.id} q={q} free={live && agent?.state === "ready"} />
            ))}
            {pending.outgoing.map((o) => (
              <OutgoingBubble key={o.id} o={o} />
            ))}
            {working && agent && <Working sessionId={s.id} since={agent.since} live={live} />}
          </>
        }
      />
      <div className="shrink-0 px-5">
        {live && agent?.pending && (
          <PermissionCard sessionId={s.id} req={agent.pending} agentName={name} onTerminal={onTerminal} />
        )}
        {live && agent?.state === "waiting" && !agent.pending && (
          <TerminalWaitCard agentName={name} onTerminal={onTerminal} />
        )}
      </div>
      <Composer
        sessionId={s.id}
        live={live}
        working={!!working}
        canSend={canSend}
        placeholder={placeholder}
      />
    </div>
  );
}

function OfflineStrip({ session: s, asOf, loaded }: { session: Session; asOf?: string; loaded: boolean }) {
  // A parked session's header has the Wake button.
  if (s.state === "parked") return <Banner>Parked{asOf ? ` — the chat as of ${clock(asOf)}` : ""}</Banner>;
  if (!loaded || running(s)) {
    return (
      <div className="flex h-7 shrink-0 items-center justify-center gap-1.5 border-b border-separator text-xs text-secondary">
        <Spinner size={10} /> connecting{asOf ? ` — as of ${clock(asOf)}` : "…"}
      </div>
    );
  }
  return null;
}

function Bubble({ text, note, tone = "" }: { text: string; note: React.ReactNode; tone?: string }) {
  return (
    <div className="flex flex-col items-end gap-1">
      <div className="selectable max-w-[85%] rounded-[14px] border border-dashed border-accent/40 px-3 py-2 whitespace-pre-wrap break-words text-label/80">
        {text}
      </div>
      <div className={`flex items-center gap-1.5 text-2xs ${tone || "text-tertiary"}`}>{note}</div>
    </div>
  );
}

function QueuedBubble({ q, free }: { q: QueuedPrompt; free: boolean }) {
  return <Bubble text={q.text} note={free ? "going in…" : "queued — goes in when the agent is free"} />;
}

function OutgoingBubble({ o }: { o: Outgoing }) {
  if (o.state === "waking") return <WakingBubble o={o} />;
  if (o.state === "failed") {
    return (
      <Bubble
        text={o.text}
        tone="text-bad"
        note={
          <>
            couldn't send{o.error ? `: ${o.error}` : ""}
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
    if (state === "done") discardPrompt(o.id);
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
