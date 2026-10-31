// Prompts on their way to a chat. Each has an id made here, so sending it
// again is harmless: the session takes an id once. A prompt stays in the
// outbox — saved with the app's storage — until its session has it, so a
// prompt sent just before the app quit or pier restarted goes out when the
// app is back, and none is sent twice. A new chat's first prompt goes the
// same way, starting the chat as it arrives.
import { create } from "zustand";
import { persist } from "zustand/middleware";
import { rpc } from "@/api/client";
import type { Attachment } from "@/api/protocol.gen";
import { Disconnected, RpcError } from "@/api/rpc";
import { trackJob } from "./jobs";

/** How a new chat runs, when the prompt starts one. */
export interface NewChat {
  agent: string;
  model?: string;
  effort?: string;
  mode?: string;
}

export interface Outgoing {
  id: string;
  sessionId: string;
  chatId: string;
  text: string;
  attachments?: Attachment[];
  /** steer: into the running turn; queue: after it. */
  mode: "steer" | "queue";
  /** Set when this prompt starts its chat. */
  create?: NewChat;
  at: number;
  /** sending: not acknowledged yet · sent: the session has it · waking: a job wakes the session to deliver it · failed. */
  state: "sending" | "sent" | "waking" | "failed";
  error?: string;
  jobId?: string;
  sentAt?: number;
}

interface Outbox {
  items: Record<string, Outgoing>;
  put: (o: Outgoing) => void;
  patch: (id: string, p: Partial<Outgoing>) => void;
  remove: (id: string) => void;
}

export const useOutbox = create<Outbox>()(
  persist(
    (set) => ({
      items: {},
      put: (o) => set((s) => ({ items: { ...s.items, [o.id]: o } })),
      patch: (id, p) =>
        set((s) => (s.items[id] ? { items: { ...s.items, [id]: { ...s.items[id]!, ...p } } } : s)),
      remove: (id) =>
        set((s) => {
          const { [id]: _, ...rest } = s.items;
          return { items: rest };
        }),
    }),
    { name: "pier.outbox.v2" },
  ),
);

/** Sends a prompt to a chat (starting it, with create). */
export function sendPrompt(p: {
  sessionId: string;
  chatId: string;
  text: string;
  attachments?: Attachment[];
  mode: "steer" | "queue";
  create?: NewChat;
}): string {
  const id = crypto.randomUUID();
  useOutbox.getState().put({ ...p, id, at: Date.now(), state: "sending" });
  void deliver(id);
  return id;
}

async function deliver(id: string) {
  const o = useOutbox.getState().items[id];
  if (!o) return;
  const { patch } = useOutbox.getState();
  patch(id, { state: "sending", error: undefined });
  const prompt = { id: o.id, text: o.text, attachments: o.attachments };
  try {
    const r = o.create
      ? await rpc.call("chat.create", { sessionId: o.sessionId, chatId: o.chatId, ...o.create, prompt })
      : await rpc.call("chat.send", { sessionId: o.sessionId, chatId: o.chatId, prompt, mode: o.mode });
    if (r.job) {
      trackJob(r.job);
      patch(id, { state: "waking", jobId: r.job.id });
    } else {
      patch(id, { state: "sent", sentAt: Date.now() });
    }
  } catch (e) {
    // pier went away mid-send: it goes out again when pier is back.
    if (e instanceof Disconnected) return;
    patch(id, {
      state: "failed",
      error: e instanceof RpcError || e instanceof Error ? e.message : String(e),
    });
  }
}

export function retryPrompt(id: string) {
  void deliver(id);
}

export function discardPrompt(id: string) {
  useOutbox.getState().remove(id);
}

/** Sends what was still on its way when pier (or the app) went away. */
export function flushOutbox() {
  for (const o of Object.values(useOutbox.getState().items)) {
    if (o.state === "sending") void deliver(o.id);
  }
}

/** How long a sent prompt stays shown before the chat reports it. */
export const sentGraceMs = 20_000;

/**
 * Settles a chat's outbox against what the chat reports: a prompt it has
 * (in its items or queue), or one sent a while ago, is the chat's to show.
 */
export function settleOutbox(chatId: string, has: Set<string>, now = Date.now()) {
  const { items, remove } = useOutbox.getState();
  for (const o of Object.values(items)) {
    if (o.chatId !== chatId || o.state !== "sent") continue;
    if (has.has(o.id) || now - (o.sentAt ?? o.at) > sentGraceMs) remove(o.id);
  }
}
