// Terminal plumbing between pier api and xterm.js: output routed to the
// terminal it belongs to, acknowledged as it renders (so a flood can't
// swamp the app), keystrokes and resizes sent back.
import { rpc } from "./client";
import type { TermData, TermExit } from "./protocol.gen";

interface Sink {
  output: (bytes: Uint8Array, done: () => void) => void;
  exit: (code: number) => void;
}

const sinks = new Map<string, Sink>();
// Output can beat term.open's reply over the wire: hold it until the
// terminal registers.
const early = new Map<string, Uint8Array[]>();

const ACK_EVERY = 32 * 1024;
const acked = new Map<string, number>();

function decode(b64: string): Uint8Array {
  const from = (Uint8Array as unknown as { fromBase64?: (s: string) => Uint8Array }).fromBase64;
  if (from) return from(b64);
  const bin = atob(b64);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

function encode(bytes: Uint8Array): string {
  let bin = "";
  for (let i = 0; i < bytes.length; i += 0x8000) {
    bin += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  }
  return btoa(bin);
}

function rendered(termId: string, n: number) {
  const total = (acked.get(termId) ?? 0) + n;
  if (total >= ACK_EVERY) {
    rpc.notify("term.ack", { termId, bytes: total });
    acked.set(termId, 0);
  } else {
    acked.set(termId, total);
  }
}

function deliver(termId: string, bytes: Uint8Array) {
  const sink = sinks.get(termId);
  if (!sink) {
    const q = early.get(termId) ?? [];
    q.push(bytes);
    early.set(termId, q);
    return;
  }
  sink.output(bytes, () => rendered(termId, bytes.length));
}

rpc.on("term.output", (p: TermData) => deliver(p.termId, decode(p.data)));
rpc.on("term.exit", (p: TermExit) => {
  sinks.get(p.termId)?.exit(p.code);
  sinks.delete(p.termId);
});

/** Routes a terminal's output to sink; returns the unregister. */
export function attachSink(termId: string, sink: Sink): () => void {
  sinks.set(termId, sink);
  for (const b of early.get(termId) ?? []) sink.output(b, () => rendered(termId, b.length));
  early.delete(termId);
  return () => {
    sinks.delete(termId);
    acked.delete(termId);
  };
}

const encoder = new TextEncoder();

export function sendInput(termId: string, data: string | Uint8Array) {
  const bytes = typeof data === "string" ? encoder.encode(data) : data;
  // Large pastes go in pieces, so one keystroke message never dwarfs the rest.
  for (let i = 0; i < bytes.length; i += 4096) {
    rpc.notify("term.input", { termId, data: encode(bytes.subarray(i, i + 4096)) });
  }
}

export function sendResize(termId: string, cols: number, rows: number) {
  rpc.notify("term.resize", { termId, cols, rows });
}

export function pause(termId: string) {
  rpc.notify("term.pause", { termId });
}

export function resume(termId: string) {
  rpc.notify("term.resume", { termId });
}
