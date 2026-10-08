// Recent load readings per session, for sparklines. Kept for as long as the
// app runs; nothing about history lives in pier.
import { useSyncExternalStore } from "react";
import type { Resources } from "@/api/protocol.gen";

const LIMIT = 60;
const series = new Map<string, { cpu: number[]; mem: number[]; at: number }>();
const listeners = new Set<() => void>();
let version = 0;

/** Records a reading, at most one per few seconds per session. */
export function recordLoad(id: string, r: Resources | undefined | null) {
  if (!r) return;
  const s = series.get(id) ?? { cpu: [], mem: [], at: 0 };
  const now = Date.now();
  if (now - s.at < 4000) return;
  s.at = now;
  s.cpu = [...s.cpu, r.cpu].slice(-LIMIT);
  s.mem = [...s.mem, r.memTotal ? (r.memUsed / r.memTotal) * 100 : 0].slice(-LIMIT);
  series.set(id, s);
  version++;
  for (const l of listeners) l();
}

export function useLoadHistory(id: string): { cpu: number[]; mem: number[] } {
  useSyncExternalStore(
    (fn) => {
      listeners.add(fn);
      return () => listeners.delete(fn);
    },
    () => version,
  );
  return series.get(id) ?? { cpu: [], mem: [] };
}
