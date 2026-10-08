// Prompts on their way to an agent. Each has an id made here, so sending it
// again is harmless: the session queues an id once. A prompt stays in the
// outbox — saved with the app's storage — until its session has it, so a
// prompt sent just before the app quit or pier restarted goes out when the
// app is back, and none is sent twice.
import { create } from "zustand";
import { persist } from "zustand/middleware";
import { rpc } from "@/api/client";
import { Disconnected, RpcError } from "@/api/rpc";
import { trackJob } from "./jobs";

export interface Outgoing {
  id: string;
  sessionId: string;
  text: string;
  /** "now" types it in mid-turn; otherwise it waits for the agent. */
  mode?: "now";
  at: number;
  /** sending: not acknowledged yet · sent: the session queued it · waking: a job wakes the session to deliver it · failed. */
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
    { name: "pier.outbox" },
  ),
);

/** Sends a prompt to a session's agent. */
export function sendPrompt(sessionId: string, text: string, mode?: "now"): string {
  const id = crypto.randomUUID();
  useOutbox.getState().put({ id, sessionId, text, mode, at: Date.now(), state: "sending" });
  void deliver(id);
  return id;
}

async function deliver(id: string) {
  const o = useOutbox.getState().items[id];
  if (!o) return;
  const { patch } = useOutbox.getState();
  patch(id, { state: "sending", error: undefined });
  try {
    const r = await rpc.call("chat.send", {
      sessionId: o.sessionId,
      promptId: o.id,
      text: o.text,
      mode: o.mode,
    });
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

/** How long a sent prompt stays shown before the session reports it. */
export const sentGraceMs = 20_000;

/**
 * Settles a session's outbox against what the session reports: a prompt
 * its queue holds, or one sent a while ago, is the session's to show now.
 */
export function settleOutbox(sessionId: string, queued: Set<string>, now = Date.now()) {
  const { items, remove } = useOutbox.getState();
  for (const o of Object.values(items)) {
    if (o.sessionId !== sessionId || o.state !== "sent") continue;
    if (queued.has(o.id) || now - (o.sentAt ?? o.at) > sentGraceMs) remove(o.id);
  }
}
