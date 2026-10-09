// What a repo's pool is up to, in words: ready, filling, or why it isn't.
import type { Repo } from "@/api/protocol.gen";

export interface PoolState {
  /** ready · filling · short (below its size, nothing filling) · none */
  kind: "ready" | "filling" | "short" | "none";
  /** One line for a list. */
  short: string;
  /** What it means, and what to do. */
  detail: string;
}

export function poolState(r: Repo, autoRefill: boolean, fillRunning: boolean): PoolState {
  if (r.poolTarget === 0) {
    return r.poolReady > 0
      ? {
          kind: "none",
          short: `pool off · ${r.poolReady} left`,
          detail: "No pool is kept; the sessions still in it go to new sessions until it is empty.",
        }
      : {
          kind: "none",
          short: "no pool",
          detail: "No pool is kept: new sessions start from scratch (or from the session image).",
        };
  }
  const of = `${r.poolReady}/${r.poolTarget}`;
  if (r.poolReady >= r.poolTarget) {
    return {
      kind: "ready",
      short: `pool ${of} ready`,
      detail: "Ready: the next new session claims one and starts in seconds.",
    };
  }
  if (r.poolFilling > 0 || fillRunning) {
    return {
      kind: "filling",
      short: `pool ${of} · filling`,
      detail: `${r.poolFilling || "Sessions"} on the way — a few minutes each. New sessions meanwhile start from the image.`,
    };
  }
  const stale =
    r.poolStale > 0 ? ` ${r.poolStale} aged out and ${r.poolStale === 1 ? "is" : "are"} being replaced.` : "";
  if (!r.checkout || r.checkout.missing) {
    return {
      kind: "short",
      short: `pool ${of} · no clone`,
      detail: `Below its size, and it can only be filled from a linked clone of ${r.name}.${stale}`,
    };
  }
  return autoRefill
    ? {
        kind: "short",
        short: `pool ${of} · refilling soon`,
        detail: `Below its size: pier refills it shortly while the app runs.${stale}`,
      }
    : {
        kind: "short",
        short: `pool ${of} · empty`,
        detail: `Below its size, and nothing refills it on its own (Settings ▸ Speed & pools ▸ Refill pools is off). Fill it now, or turn refilling on.${stale}`,
      };
}
