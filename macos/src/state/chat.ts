// A session's chat: the snapshot chat.watch opens with, folded together
// with every change after it and the older pages chat.history brings in.
// One store per session, shared by every view of it, kept a little after
// the last one goes so switching sessions doesn't refetch.
import { useSyncExternalStore } from "react";
import { rpc } from "@/api/client";
import type { AgentDetail, ChatChange, ChatItem, ChatSnapshot } from "@/api/protocol.gen";
import type { Watch } from "@/api/rpc";

export interface ChatState {
  items: ChatItem[];
  /** Older items exist on the server. */
  more: boolean;
  agent?: AgentDetail;
  live: boolean;
  asOf?: string;
  partial: boolean;
  loaded: boolean;
  loadingOlder: boolean;
  error?: unknown;
}

export const emptyChat: ChatState = {
  items: [],
  more: false,
  live: false,
  partial: false,
  loaded: false,
  loadingOlder: false,
};

export function fromSnapshot(s: ChatSnapshot): ChatState {
  return {
    items: s.items,
    more: s.more,
    agent: s.agent,
    live: s.live,
    asOf: s.asOf,
    partial: !!s.partial,
    loaded: true,
    loadingOlder: false,
  };
}

/** Folds one change into the chat. */
export function applyChange(st: ChatState, c: ChatChange): ChatState {
  const partial = st.partial || !!c.partial;
  switch (c.op) {
    case "reset":
      return { ...st, items: c.items ?? [], more: !!c.more, partial: !!c.partial };
    case "append": {
      const have = new Set(st.items.map((i) => i.id));
      const add = (c.items ?? []).filter((i) => !have.has(i.id));
      return { ...st, items: [...st.items, ...add], partial };
    }
    case "update": {
      const by = new Map((c.items ?? []).map((i) => [i.id, i]));
      return { ...st, items: st.items.map((i) => by.get(i.id) ?? i), partial };
    }
    case "agent": {
      const next = { ...st, agent: c.agent };
      if (c.live !== undefined) {
        next.live = c.live;
        next.asOf = c.live ? undefined : c.asOf;
      }
      return next;
    }
    default:
      return st;
  }
}

/** Puts an older page in front. */
export function prependPage(st: ChatState, items: ChatItem[], more: boolean): ChatState {
  const have = new Set(st.items.map((i) => i.id));
  return { ...st, items: [...items.filter((i) => !have.has(i.id)), ...st.items], more, loadingOlder: false };
}

class ChatStore {
  state: ChatState = emptyChat;
  private readonly listeners = new Set<() => void>();
  private watch: Watch<"chat"> | null = null;
  private closeTimer: ReturnType<typeof setTimeout> | null = null;

  constructor(readonly sessionId: string) {}

  private set(st: ChatState) {
    this.state = st;
    for (const l of this.listeners) l();
  }

  subscribe = (fn: () => void) => {
    this.listeners.add(fn);
    if (this.closeTimer) {
      clearTimeout(this.closeTimer);
      this.closeTimer = null;
    }
    if (!this.watch) {
      this.watch = rpc.watch(
        "chat",
        { id: this.sessionId },
        (d, snapshot) =>
          this.set(snapshot ? fromSnapshot(d as ChatSnapshot) : applyChange(this.state, d as ChatChange)),
        (error) => this.set({ ...this.state, error, loaded: true }),
      );
    }
    return () => {
      this.listeners.delete(fn);
      if (this.listeners.size === 0) {
        this.closeTimer = setTimeout(() => {
          this.watch?.close();
          this.watch = null;
          this.state = emptyChat;
          stores.delete(this.sessionId);
        }, 60_000);
      }
    };
  };

  get = () => this.state;

  async loadOlder() {
    const st = this.state;
    if (!st.more || st.loadingOlder || st.items.length === 0) return;
    this.set({ ...st, loadingOlder: true });
    try {
      const page = await rpc.call("chat.history", {
        sessionId: this.sessionId,
        before: st.items[0]!.id,
        limit: 200,
      });
      this.set(prependPage(this.state, page.items, page.more));
    } catch {
      this.set({ ...this.state, loadingOlder: false });
    }
  }
}

const stores = new Map<string, ChatStore>();

function store(sessionId: string): ChatStore {
  let s = stores.get(sessionId);
  if (!s) {
    s = new ChatStore(sessionId);
    stores.set(sessionId, s);
  }
  return s;
}

/** A session's chat, live. */
export function useChat(sessionId: string): ChatState & { loadOlder: () => void } {
  const s = store(sessionId);
  const st = useSyncExternalStore(s.subscribe, s.get);
  return { ...st, loadOlder: () => void s.loadOlder() };
}
