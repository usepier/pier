// A session's chats, live. chats.watch keeps the list (state, what each
// waits on, queues); chat.watch one chat's items, with the text the agent
// is writing streaming in. Stores are shared by every view of a session or
// chat and kept a minute after the last one goes, so switching back is
// instant.
import { useSyncExternalStore } from "react";
import { rpc } from "@/api/client";
import type {
  ChatChange,
  ChatInfo,
  ChatItem,
  ChatSnapshot,
  ChatsChange,
  ChatsSnapshot,
} from "@/api/protocol.gen";
import type { Watch } from "@/api/rpc";

export interface ChatState {
  items: ChatItem[];
  /** Older items exist. */
  more: boolean;
  chat?: ChatInfo;
  /** Items being written, as far as they got. */
  streaming: ChatItem[];
  live: boolean;
  asOf?: string;
  loaded: boolean;
  loadingOlder: boolean;
  error?: unknown;
}

export const emptyChat: ChatState = {
  items: [],
  more: false,
  streaming: [],
  live: false,
  loaded: false,
  loadingOlder: false,
};

export function fromSnapshot(s: ChatSnapshot): ChatState {
  return {
    items: s.items,
    more: s.more,
    chat: s.chat,
    streaming: s.streaming ?? [],
    live: s.live,
    asOf: s.asOf,
    loaded: true,
    loadingOlder: false,
  };
}

/** Folds one change into a chat. */
export function applyChange(st: ChatState, c: ChatChange): ChatState {
  switch (c.op) {
    case "reset":
      return { ...st, items: c.items ?? [], more: !!c.more, streaming: [] };
    case "append": {
      const add = c.items ?? [];
      const have = new Set(st.items.map((i) => i.id));
      const done = new Set(add.map((i) => i.id));
      return {
        ...st,
        items: [...st.items, ...add.filter((i) => !have.has(i.id))],
        streaming: st.streaming.filter((i) => !done.has(i.id)),
      };
    }
    case "update": {
      const by = new Map((c.items ?? []).map((i) => [i.id, i]));
      return {
        ...st,
        items: st.items.map((i) => by.get(i.id) ?? i),
        streaming: st.streaming.filter((i) => !by.has(i.id)),
      };
    }
    case "chat": {
      const next: ChatState = { ...st, chat: c.chat ?? st.chat };
      if (c.live !== undefined) {
        next.live = c.live;
        next.asOf = c.live ? undefined : c.asOf;
        if (!c.live) next.streaming = [];
      }
      // A turn that ended leaves nothing being written.
      if (c.chat && c.chat.state !== "working" && c.chat.state !== "waiting") next.streaming = [];
      return next;
    }
    case "delta": {
      const have = new Set(st.items.map((i) => i.id));
      let streaming = st.streaming;
      for (const d of c.deltas ?? []) {
        if (have.has(d.id)) continue;
        const at = streaming.findIndex((i) => i.id === d.id);
        if (at < 0) {
          streaming = [
            ...streaming,
            { id: d.id, kind: d.kind, time: new Date().toISOString(), text: d.text },
          ];
        } else {
          const it = streaming[at]!;
          streaming = [
            ...streaming.slice(0, at),
            { ...it, text: (it.text ?? "") + d.text },
            ...streaming.slice(at + 1),
          ];
        }
      }
      return { ...st, streaming };
    }
    case "streaming":
      return { ...st, streaming: c.items ?? [] };
    default:
      return st;
  }
}

/** Puts an older page in front. */
export function prependPage(st: ChatState, items: ChatItem[], more: boolean): ChatState {
  const have = new Set(st.items.map((i) => i.id));
  return { ...st, items: [...items.filter((i) => !have.has(i.id)), ...st.items], more, loadingOlder: false };
}

/** A store kept alive while used, and a minute after. */
abstract class Live<S> {
  state: S;
  private readonly listeners = new Set<() => void>();
  private closeTimer: ReturnType<typeof setTimeout> | null = null;
  protected watch: { close(): void } | null = null;

  constructor(
    initial: S,
    private readonly onIdle: () => void,
  ) {
    this.state = initial;
  }

  protected abstract open(): { close(): void };

  protected set(st: S) {
    this.state = st;
    for (const l of this.listeners) l();
  }

  subscribe = (fn: () => void) => {
    this.listeners.add(fn);
    if (this.closeTimer) {
      clearTimeout(this.closeTimer);
      this.closeTimer = null;
    }
    if (!this.watch) this.watch = this.open();
    return () => {
      this.listeners.delete(fn);
      if (this.listeners.size === 0) {
        this.closeTimer = setTimeout(() => {
          this.watch?.close();
          this.watch = null;
          this.onIdle();
        }, 60_000);
      }
    };
  };

  get = () => this.state;
}

class ChatStore extends Live<ChatState> {
  constructor(
    readonly sessionId: string,
    readonly chatId: string,
    onIdle: () => void,
  ) {
    super(emptyChat, onIdle);
  }

  protected open(): Watch<"chat"> {
    return rpc.watch(
      "chat",
      { sessionId: this.sessionId, chatId: this.chatId },
      (d, snapshot) =>
        this.set(snapshot ? fromSnapshot(d as ChatSnapshot) : applyChange(this.state, d as ChatChange)),
      (error) => this.set({ ...this.state, error, loaded: true }),
    );
  }

  async loadOlder() {
    const st = this.state;
    if (!st.more || st.loadingOlder || st.items.length === 0) return;
    this.set({ ...st, loadingOlder: true });
    try {
      const page = await rpc.call("chat.history", {
        sessionId: this.sessionId,
        chatId: this.chatId,
        before: st.items[0]!.id,
        limit: 200,
      });
      this.set(prependPage(this.state, page.items, page.more));
    } catch {
      this.set({ ...this.state, loadingOlder: false });
    }
  }
}

export interface ChatsState {
  chats: ChatInfo[];
  live: boolean;
  loaded: boolean;
  error?: unknown;
}

const emptyChats: ChatsState = { chats: [], live: false, loaded: false };

class ChatsStore extends Live<ChatsState> {
  constructor(
    readonly sessionId: string,
    onIdle: () => void,
  ) {
    super(emptyChats, onIdle);
  }

  protected open(): Watch<"chats"> {
    return rpc.watch(
      "chats",
      { id: this.sessionId },
      (d) => {
        const c = d as ChatsSnapshot | ChatsChange;
        this.set({ chats: c.chats ?? [], live: c.live, loaded: true });
      },
      (error) => this.set({ ...this.state, error, loaded: true }),
    );
  }
}

const chatStores = new Map<string, ChatStore>();
const listStores = new Map<string, ChatsStore>();

function chatStore(sessionId: string, chatId: string): ChatStore {
  const key = `${sessionId}/${chatId}`;
  let s = chatStores.get(key);
  if (!s) {
    s = new ChatStore(sessionId, chatId, () => chatStores.delete(key));
    chatStores.set(key, s);
  }
  return s;
}

function listStore(sessionId: string): ChatsStore {
  let s = listStores.get(sessionId);
  if (!s) {
    s = new ChatsStore(sessionId, () => listStores.delete(sessionId));
    listStores.set(sessionId, s);
  }
  return s;
}

/** One chat, live. */
export function useChat(sessionId: string, chatId: string): ChatState & { loadOlder: () => void } {
  const s = chatStore(sessionId, chatId);
  const st = useSyncExternalStore(s.subscribe, s.get);
  return { ...st, loadOlder: () => void s.loadOlder() };
}

/** A session's chats, live. */
export function useChats(sessionId: string): ChatsState {
  const s = listStore(sessionId);
  return useSyncExternalStore(s.subscribe, s.get);
}

/** Whether a chat is mid-turn or waiting: busy, in a list. */
export function chatBusy(c: { state: string }): boolean {
  return c.state === "working" || c.state === "waiting";
}
