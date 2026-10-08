// React access to pier: the connection status, and subscriptions shared by
// every component that shows the same data. A subscription opens with its
// first viewer and closes a little after its last, so switching views
// doesn't churn the server.
import { useSyncExternalStore } from "react";
import { rpc } from "./client";
import type { Watches } from "./protocol.gen";
import type { Status, Watch } from "./rpc";

export function useStatus(): Status {
  return useSyncExternalStore(
    (fn) => rpc.subscribeStatus(fn),
    () => rpc.status,
  );
}

interface Live<T> {
  data: T | undefined;
  error: unknown;
}

type Snap<T extends keyof Watches> = Watches[T]["snapshot"];
type Data<T extends keyof Watches> = Watches[T]["data"];
/** Folds an update into the data so far. */
type Reduce<T extends keyof Watches> = (prev: Snap<T> | undefined, next: Data<T>) => Snap<T>;

class Shared<T extends keyof Watches> {
  state: Live<Snap<T>> = { data: undefined, error: undefined };
  private readonly listeners = new Set<() => void>();
  private watch: Watch<T> | null = null;
  private closeTimer: ReturnType<typeof setTimeout> | null = null;

  constructor(
    readonly topic: T,
    readonly params: Watches[T]["params"],
    private readonly graceMs: number,
    private readonly reduce?: Reduce<T>,
  ) {}

  subscribe = (fn: () => void) => {
    this.listeners.add(fn);
    if (this.closeTimer) {
      clearTimeout(this.closeTimer);
      this.closeTimer = null;
    }
    if (!this.watch) {
      this.watch = rpc.watch(
        this.topic,
        this.params,
        (d, snapshot) => {
          // Without a reducer an update is the whole new data.
          const data = this.reduce && !snapshot ? this.reduce(this.state.data, d as Data<T>) : (d as Snap<T>);
          this.state = { data, error: undefined };
          for (const l of this.listeners) l();
        },
        (e) => {
          this.state = { ...this.state, error: e };
          for (const l of this.listeners) l();
        },
      );
    }
    return () => {
      this.listeners.delete(fn);
      if (this.listeners.size === 0) {
        this.closeTimer = setTimeout(() => {
          this.watch?.close();
          this.watch = null;
          this.state = { data: undefined, error: undefined };
          shared.delete(key(this.topic, this.params));
        }, this.graceMs);
      }
    };
  };

  get = () => this.state;
}

const shared = new Map<string, Shared<keyof Watches>>();

function key(topic: string, params: unknown) {
  return `${topic}:${JSON.stringify(params ?? {})}`;
}

export interface WatchOptions<T extends keyof Watches> {
  /** false skips subscribing (nothing to watch yet). */
  enabled?: boolean;
  /** How long the subscription outlives its last viewer. */
  graceMs?: number;
  /** Folds each update into the previous data (append-style topics). */
  reduce?: Reduce<T>;
}

/** Live data for a topic; undefined until the first snapshot. */
const idle: Live<never> = { data: undefined, error: undefined };
const noop = () => () => {};

export function useWatch<T extends keyof Watches>(
  topic: T,
  params: Watches[T]["params"],
  opts: WatchOptions<T> = {},
): Live<Snap<T>> {
  const enabled = opts.enabled ?? true;
  const k = key(topic, params);
  let s = shared.get(k) as Shared<T> | undefined;
  if (!s && enabled) {
    s = new Shared(topic, params, opts.graceMs ?? 30_000, opts.reduce);
    shared.set(k, s as unknown as Shared<keyof Watches>);
  }
  return useSyncExternalStore(s && enabled ? s.subscribe : noop, s && enabled ? s.get : () => idle);
}
