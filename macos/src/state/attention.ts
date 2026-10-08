// What needs the user: an agent waiting on them, a turn they haven't seen
// finish, a setup or create that failed. pier says what needs input or
// failed; what is unseen is this app's to know.
import { create } from "zustand";
import { persist } from "zustand/middleware";
import type { Session } from "@/api/protocol.gen";

interface Seen {
  /** Per session, the agent state (its since) last seen. */
  seen: Record<string, string>;
  markSeen: (sessionId: string, since: string) => void;
}

export const useSeen = create<Seen>()(
  persist(
    (set) => ({
      seen: {},
      markSeen: (id, since) => set((s) => (s.seen[id] === since ? s : { seen: { ...s.seen, [id]: since } })),
    }),
    { name: "pier.seen" },
  ),
);

export type Reason = "needs_input" | "finished" | "setup_failed" | "create_failed";

/** Why a session needs the user, if it does. */
export function attentionOf(s: Session, seen: Record<string, string>): Reason | undefined {
  if (s.pooled) return undefined;
  if (s.attention) return s.attention as Reason;
  const a = s.agent;
  // A finished turn leaves its last message; an agent that only started
  // has none.
  if (a && a.state === "ready" && a.last && a.since > (seen[s.id] ?? "")) return "finished";
  return undefined;
}

export const reasonText: Record<Reason, string> = {
  needs_input: "needs you",
  finished: "finished",
  setup_failed: "setup failed",
  create_failed: "create failed",
};

/** Sessions needing the user, most urgent first. */
export function needingAttention(sessions: Session[], seen: Record<string, string>) {
  const rank: Record<Reason, number> = { needs_input: 0, create_failed: 1, setup_failed: 2, finished: 3 };
  return sessions
    .map((s) => ({ session: s, reason: attentionOf(s, seen) }))
    .filter((x): x is { session: Session; reason: Reason } => !!x.reason)
    .sort((a, b) => rank[a.reason] - rank[b.reason] || a.session.name.localeCompare(b.session.name));
}
