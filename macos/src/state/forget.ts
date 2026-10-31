// What this app keeps per session — drafts, open tabs, unsaved edits,
// review comments, unsent prompts, chats not started, what was seen — is
// forgotten once the session has been missing from the overview for a
// month. Not at once: switching to another account and back keeps it.
import { useEffect } from "react";
import { create } from "zustand";
import { persist } from "zustand/middleware";
import { useSeen } from "./attention";
import { useBuffers } from "./buffers";
import { useComments } from "./comments";
import { useDocs } from "./docs";
import { useDrafts } from "./drafts";
import { useNewChats } from "./newChats";
import { useOutbox } from "./outbox";
import { useOverview } from "./overview";

export const keepFor = 30 * 24 * 3600e3;
/** A listed session's time is written at most this often. */
const touchEvery = 3600e3;

interface Known {
  /** When each session was last listed (or, for one kept from before, first met). */
  at: Record<string, number>;
}

export const useKnown = create<Known>()(persist(() => ({ at: {} }), { name: "pier.known" }));

/** The session a per-session key belongs to: "<id>", "<id>/<chat>", "<id>\n<path>". */
const sessionOf = (key: string) => key.split(/[/\n]/)[0] ?? key;

function without<T>(m: Record<string, T>, gone: Set<string>): Record<string, T> {
  return Object.fromEntries(Object.entries(m).filter(([k]) => !gone.has(sessionOf(k))));
}

/** Everything kept, by session. */
function referenced(): Set<string> {
  const ids = new Set<string>();
  const add = (keys: string[]) => {
    for (const k of keys) ids.add(sessionOf(k));
  };
  add(Object.keys(useDrafts.getState().drafts));
  add(Object.keys(useSeen.getState().seen));
  add(Object.keys(useBuffers.getState().buffers));
  add(Object.keys(useComments.getState().comments));
  add(Object.keys(useDocs.getState().docs));
  add(Object.keys(useDocs.getState().active));
  add(Object.keys(useNewChats.getState().chats));
  for (const o of Object.values(useOutbox.getState().items)) ids.add(o.sessionId);
  return ids;
}

/** Notes the sessions listed now and forgets those missing for keepFor. */
export function forgetGone(listed: string[], now = Date.now()) {
  const before = useKnown.getState().at;
  const at = { ...before };
  let changed = false;
  for (const id of listed) {
    if (now - (at[id] ?? 0) > touchEvery) {
      at[id] = now;
      changed = true;
    }
  }
  for (const id of referenced()) {
    if (!(id in at)) {
      at[id] = now;
      changed = true;
    }
  }
  const gone = new Set(Object.keys(at).filter((id) => now - at[id]! > keepFor));
  for (const id of gone) delete at[id];
  if (changed || gone.size) useKnown.setState({ at });
  if (!gone.size) return;
  useDrafts.setState((s) => ({ drafts: without(s.drafts, gone) }));
  useSeen.setState((s) => ({ seen: without(s.seen, gone) }));
  useBuffers.setState((s) => ({ buffers: without(s.buffers, gone) }));
  useComments.setState((s) => ({ comments: without(s.comments, gone) }));
  useDocs.setState((s) => ({ docs: without(s.docs, gone), active: without(s.active, gone) }));
  useNewChats.setState((s) => ({ chats: without(s.chats, gone) }));
  useOutbox.setState((s) => ({
    items: Object.fromEntries(Object.entries(s.items).filter(([, o]) => !gone.has(o.sessionId))),
  }));
}

/** Runs forgetGone on every overview that listed sessions without failing. */
export function useForgetGone() {
  const o = useOverview();
  useEffect(() => {
    if (o && !o.error) forgetGone(o.sessions.map((s) => s.id));
  }, [o]);
}
