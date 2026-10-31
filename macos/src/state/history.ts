// Recent load readings per session, for graphs. A session sends the last
// few minutes when its environment is first watched, then a reading every
// few seconds; the app keeps them for as long as it runs.
import { useEffect, useSyncExternalStore } from "react";
import { useWatch } from "@/api/live";
import type { Env, Resources, Session } from "@/api/protocol.gen";
import { readable } from "@/lib/session";

/** How far back a graph goes. */
export const LOAD_WINDOW_MS = 10 * 60_000;

export interface Reading {
  t: number;
  cpu: number;
  mem: number;
}

const series = new Map<string, Reading[]>();
const listeners = new Set<() => void>();
let version = 0;

function add(id: string, readings: Reading[]) {
  const merged = [...(series.get(id) ?? []), ...readings].sort((a, b) => a.t - b.t);
  const cut = Date.now() - LOAD_WINDOW_MS;
  const out: Reading[] = [];
  for (const r of merged) {
    const last = out[out.length - 1];
    // The same reading comes again with every watch that starts.
    if (r.t < cut || (last && r.t - last.t < 2000)) continue;
    out.push(r);
  }
  series.set(id, out);
  version++;
  for (const l of listeners) l();
}

/** Takes the readings an environment message carries: its history, and its newest. */
export function feedLoad(id: string, e: Env | undefined) {
  if (!e) return;
  const readings = (e.load ?? []).map((p) => ({ t: Date.parse(p.at), cpu: p.cpu, mem: p.memUsed }));
  if (e.resources) readings.push({ t: Date.parse(e.at), cpu: e.resources.cpu, mem: e.resources.memUsed });
  if (readings.length) add(id, readings);
}

/** A session's load as it comes: the readings for its graphs, and the newest. */
export function useLoad(s: Session): { readings: Reading[]; now: Resources | undefined } {
  const env = useWatch("env", { id: s.id }, { enabled: readable(s) });
  useEffect(() => feedLoad(s.id, env.data), [env.data, s.id]);
  useSyncExternalStore(
    (fn) => {
      listeners.add(fn);
      return () => listeners.delete(fn);
    },
    () => version,
  );
  return { readings: series.get(s.id) ?? [], now: env.data?.resources ?? s.resources };
}
