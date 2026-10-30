// What a repo's pool is up to, in words: ready, filling, or why it isn't.
import type { Repo } from "@/api/protocol.gen";
import { age } from "@/lib/format";

export interface PoolState {
  /** ready · filling · failed (its last fill failed) · short (below its size, nothing filling) · none */
  kind: "ready" | "filling" | "failed" | "short" | "none";
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
          detail: "Pool off — the ones left go to new sessions.",
        }
      : {
          kind: "none",
          short: "no pool",
          detail: "No pool kept.",
        };
  }
  const of = `${r.poolReady}/${r.poolTarget}`;
  if (r.needsImage) {
    return {
      kind: "none",
      short: `pool ${of} · waits for the image`,
      detail: "Waits for the session image.",
    };
  }
  if (r.poolReady >= r.poolTarget) {
    return {
      kind: "ready",
      short: `pool ${of} ready`,
      detail: "Ready for the next session.",
    };
  }
  if (r.poolFilling > 0 || fillRunning) {
    return {
      kind: "filling",
      short: `pool ${of} · filling`,
      detail: "Filling — a few minutes.",
    };
  }
  if (r.poolError) {
    return {
      kind: "failed",
      short: `pool ${of} · last fill failed`,
      detail: `Last fill failed ${age(r.poolError.at)} ago: ${r.poolError.message}.`,
    };
  }
  if (!r.checkout || r.checkout.missing) {
    return {
      kind: "short",
      short: `pool ${of} · no clone`,
      detail: `Needs a linked clone of ${r.name} to fill.`,
    };
  }
  return autoRefill
    ? {
        kind: "short",
        short: `pool ${of} · refilling soon`,
        detail: "Refilling shortly.",
      }
    : {
        kind: "short",
        short: `pool ${of} · empty`,
        detail: "Not refilling on its own — fill it now.",
      };
}
