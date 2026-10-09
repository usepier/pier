// The JSON-RPC client for `pier api`: requests with cancellation and
// progress, server notifications, subscriptions that survive a restart of
// the pier process, and the connection's status for the UI.
import type {
  ErrorData,
  ErrorKind,
  Event,
  InitializeResult,
  Methods,
  ServerNotifications,
  SubEvent,
  Watches,
  WatchResult,
} from "./protocol.gen";
import { PROTOCOL_VERSION } from "./protocol.gen";
import type { Transport } from "./transport";

export class RpcError extends Error {
  readonly kind: ErrorKind;
  readonly code: number;
  readonly login?: string[];

  constructor(code: number, message: string, data?: ErrorData) {
    super(message);
    this.code = code;
    this.kind = data?.kind ?? "internal";
    this.login = data?.login;
  }
}

/** The connection ended before the call answered; its outcome is unknown. */
export class Disconnected extends RpcError {
  constructor() {
    super(-1, "pier stopped before answering", { kind: "unreachable", message: "pier stopped" });
  }
}

/** incompatible: the pier answering isn't the one this app was built with. */
export type Status = "starting" | "ready" | "restarting" | "failed" | "incompatible";

/** A release version without its v and with its patch: "v1.2" → "1.2.0"; undefined for a dev build. */
function release(v: string): string | undefined {
  const m = /^v?(\d+)\.(\d+)(?:\.(\d+))?$/.exec(v);
  return m ? `${m[1]}.${m[2]}.${m[3] ?? 0}` : undefined;
}

/** A release app runs only the pier released with it; dev builds run any. */
export function compatible(app: string, pier: string): boolean {
  const a = release(app);
  const p = release(pier);
  return !a || !p || a === p;
}

interface Pending {
  resolve: (v: unknown) => void;
  reject: (e: unknown) => void;
  onProgress?: (e: Event) => void;
}

export interface CallOptions {
  signal?: AbortSignal;
  onProgress?: (e: Event) => void;
}

type Params<M extends keyof Methods> = Methods[M]["params"];
type Result<M extends keyof Methods> = Methods[M]["result"];

/** What a watch delivers: its snapshot first (snapshot true), then its events. */
export type WatchData<T extends keyof Watches> = Watches[T]["snapshot"] | Watches[T]["data"];

/** One live subscription. It resubscribes by itself after a restart or a missed event. */
export class Watch<T extends keyof Watches> {
  private sub = "";
  private seq = 0;
  private closed = false;
  private reopening = false;

  constructor(
    private readonly rpc: Rpc,
    readonly topic: T,
    private readonly params: Watches[T]["params"],
    private readonly onData: (d: WatchData<T>, snapshot: boolean) => void,
    private readonly onError: (e: unknown) => void,
  ) {}

  async open() {
    if (this.closed) return;
    try {
      const method = `${this.topic}.watch` as keyof Methods;
      const r = (await this.rpc.call(method, this.params as never)) as WatchResult<Watches[T]["snapshot"]>;
      if (this.closed) {
        this.rpc.notify("sub.cancel", { sub: r.sub });
        return;
      }
      this.rpc.dropWatch(this.sub);
      this.sub = r.sub;
      this.seq = r.seq;
      this.rpc.adoptWatch(r.sub, this as unknown as Watch<keyof Watches>);
      this.onData(r.snapshot, true);
    } catch (e) {
      if (!(e instanceof Disconnected)) this.onError(e);
    }
  }

  /** @internal */
  receive(ev: SubEvent) {
    if (this.reopening) return;
    if (ev.seq !== this.seq + 1) {
      // Missed one (or pier says this client fell behind): end this
      // subscription and start over from a fresh snapshot.
      this.reopening = true;
      this.rpc.notify("sub.cancel", { sub: this.sub });
      this.rpc.dropWatch(this.sub);
      void this.open().finally(() => {
        this.reopening = false;
      });
      return;
    }
    this.seq = ev.seq;
    this.onData(ev.data as Watches[T]["data"], false);
  }

  close() {
    this.closed = true;
    if (this.sub) {
      this.rpc.notify("sub.cancel", { sub: this.sub });
      this.rpc.dropWatch(this.sub);
    }
    this.rpc.forget(this as unknown as Watch<keyof Watches>);
  }
}

export class Rpc {
  private transport: Transport | null = null;
  private nextId = 0;
  private readonly pending = new Map<number, Pending>();
  private readonly watchesBySub = new Map<string, Watch<keyof Watches>>();
  private readonly watches = new Set<Watch<keyof Watches>>();
  private readonly handlers = new Map<string, Set<(p: unknown) => void>>();
  private readonly statusListeners = new Set<() => void>();
  private crashes: number[] = [];
  status: Status = "starting";
  info: InitializeResult | null = null;
  lastExit = "";

  constructor(
    private readonly makeTransport: () => Transport,
    private readonly client: { name: string; version: string },
  ) {}

  /** Connects and initializes; on loss, reconnects with backoff. */
  async start(): Promise<void> {
    const t = this.makeTransport();
    this.transport = t;
    try {
      await t.start(
        (line) => this.receive(line),
        (reason) => this.lost(t, reason),
      );
      this.info = await this.call("initialize", {
        protocol: PROTOCOL_VERSION,
        client: this.client,
      });
      if (this.info.protocol !== PROTOCOL_VERSION) {
        throw new Error(`pier speaks protocol ${this.info.protocol}, the app ${PROTOCOL_VERSION}`);
      }
      if (!compatible(this.client.version, this.info.version)) {
        // Starting it again won't change which pier answers.
        this.transport = null;
        void t.stop();
        this.lastExit = `This app is ${this.client.version} but the pier it runs is ${this.info.version}`;
        this.setStatus("incompatible");
        return;
      }
      this.setStatus("ready");
      for (const w of this.watches) void w.open();
    } catch (e) {
      this.lost(t, e instanceof Error ? e.message : String(e));
    }
  }

  private lost(t: Transport, reason: string) {
    if (this.transport !== t) return; // an older connection's late goodbye
    this.transport = null;
    this.lastExit = reason;
    for (const p of this.pending.values()) p.reject(new Disconnected());
    this.pending.clear();
    this.watchesBySub.clear();
    const now = Date.now();
    this.crashes = [...this.crashes.filter((c) => now - c < 60_000), now];
    if (this.crashes.length >= 5) {
      this.setStatus("failed");
      return;
    }
    this.setStatus("restarting");
    const delay = Math.min(10_000, 500 * 2 ** (this.crashes.length - 1));
    setTimeout(() => void this.start(), delay);
  }

  /** Starts over after the UI's "Restart" (the failed state stops retrying). */
  restart() {
    this.crashes = [];
    void this.transport?.stop();
    this.transport = null;
    this.setStatus("starting");
    void this.start();
  }

  async stop() {
    const t = this.transport;
    this.transport = null;
    await t?.stop();
  }

  private receive(line: string) {
    let m: {
      id?: number | string;
      method?: string;
      params?: unknown;
      result?: unknown;
      error?: { code: number; message: string; data?: ErrorData };
    };
    try {
      m = JSON.parse(line);
    } catch {
      return;
    }
    if (m.method) {
      if (m.method === "sub.event") {
        const ev = m.params as SubEvent;
        this.watchesBySub.get(ev.sub)?.receive(ev);
        return;
      }
      if (m.method === "$/progress") {
        const p = m.params as ServerNotifications["$/progress"];
        this.pending.get(Number(p.id))?.onProgress?.(p.event);
        return;
      }
      for (const h of this.handlers.get(m.method) ?? []) h(m.params);
      return;
    }
    const id = Number(m.id);
    const p = this.pending.get(id);
    if (!p) return;
    this.pending.delete(id);
    if (m.error) p.reject(new RpcError(m.error.code, m.error.message, m.error.data));
    else p.resolve(m.result);
  }

  call<M extends keyof Methods>(method: M, params: Params<M>, opts: CallOptions = {}): Promise<Result<M>> {
    const t = this.transport;
    if (!t) return Promise.reject(new Disconnected());
    const id = ++this.nextId;
    return new Promise<Result<M>>((resolve, reject) => {
      this.pending.set(id, { resolve: resolve as (v: unknown) => void, reject, onProgress: opts.onProgress });
      if (opts.signal) {
        if (opts.signal.aborted) {
          this.pending.delete(id);
          reject(new RpcError(-32800, "cancelled", { kind: "cancelled", message: "cancelled" }));
          return;
        }
        opts.signal.addEventListener("abort", () => this.notify("$/cancelRequest", { id }), { once: true });
      }
      t.send(JSON.stringify({ jsonrpc: "2.0", id, method, params })).catch(() => {
        this.pending.delete(id);
        reject(new Disconnected());
      });
    });
  }

  notify(method: string, params: unknown) {
    void this.transport?.send(JSON.stringify({ jsonrpc: "2.0", method, params })).catch(() => {});
  }

  /** Listens to a server notification; returns the unsubscribe. */
  on<N extends keyof ServerNotifications>(method: N, h: (p: ServerNotifications[N]) => void): () => void {
    const set = this.handlers.get(method) ?? new Set();
    set.add(h as (p: unknown) => void);
    this.handlers.set(method, set);
    return () => set.delete(h as (p: unknown) => void);
  }

  /** Opens a subscription. Close it when its consumer goes away. */
  watch<T extends keyof Watches>(
    topic: T,
    params: Watches[T]["params"],
    onData: (d: WatchData<T>, snapshot: boolean) => void,
    onError: (e: unknown) => void = () => {},
  ): Watch<T> {
    const w = new Watch(this, topic, params, onData, onError);
    this.watches.add(w as unknown as Watch<keyof Watches>);
    if (this.status === "ready") void w.open();
    return w;
  }

  /** @internal */
  adoptWatch(sub: string, w: Watch<keyof Watches>) {
    this.watchesBySub.set(sub, w);
  }
  /** @internal */
  dropWatch(sub: string) {
    if (sub) this.watchesBySub.delete(sub);
  }
  /** @internal */
  forget(w: Watch<keyof Watches>) {
    this.watches.delete(w);
  }

  subscribeStatus(fn: () => void): () => void {
    this.statusListeners.add(fn);
    return () => this.statusListeners.delete(fn);
  }

  private setStatus(s: Status) {
    this.status = s;
    for (const fn of this.statusListeners) fn();
  }
}
