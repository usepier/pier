// What needs the user: a chat waiting on them, a turn they haven't seen
// finish, a setup or create that failed. pier says what needs input or
// failed; what is unseen is this app's to know.
import { create } from "zustand";
import { persist } from "zustand/middleware";
import type { Session } from "@/api/protocol.gen";

interface Seen {
  /** Per chat ("<session>/<chat>"), the state (its since) last seen. */
  seen: Record<string, string>;
  markSeen: (key: string, since: string) => void;
}

export const seenKey = (sessionId: string, chatId: string) => `${sessionId}/${chatId}`;

export const useSeen = create<Seen>()(
  persist(
    (set) => ({
      seen: {},
      markSeen: (id, since) => set((s) => (s.seen[id] === since ? s : { seen: { ...s.seen, [id]: since } })),
    }),
    { name: "pier.seen" },
  ),
);

export type Reason =
  | "needs_input"
  | "finished"
  | "setup_failed"
  | "create_failed"
  | "ci_failing"
  | "changes_requested"
  | "merged_cleanup"
  | "ready_to_merge";

const fromPier = new Set<string>([
  "needs_input",
  "setup_failed",
  "create_failed",
  "ci_failing",
  "changes_requested",
  "merged_cleanup",
  "ready_to_merge",
]);

/** Why a session needs the user, if it does. */
export function attentionOf(s: Session, seen: Record<string, string>): Reason | undefined {
  if (s.pooled) return undefined;
  const pier = s.attention && fromPier.has(s.attention) ? (s.attention as Reason) : undefined;
  if (pier === "needs_input" || pier === "create_failed" || pier === "setup_failed") return pier;
  // A finished turn leaves its last message; a chat that only started has
  // none. An unseen one comes before what the pull request says.
  if (unseenChat(s, seen)) return "finished";
  return pier;
}

/** A chat of the session that finished a turn the user hasn't seen. */
export function unseenChat(s: Session, seen: Record<string, string>) {
  return s.chats?.find((c) => c.state === "ready" && c.last && c.since > (seen[seenKey(s.id, c.id)] ?? ""));
}

export const reasonText: Record<Reason, string> = {
  needs_input: "needs you",
  finished: "finished",
  setup_failed: "setup failed",
  create_failed: "create failed",
  ci_failing: "CI failing",
  changes_requested: "changes requested",
  merged_cleanup: "merged",
  ready_to_merge: "ready to merge",
};

/** How a reason reads at a glance. */
export const reasonTone: Record<Reason, "warn" | "bad" | "accent" | "ok" | "merged"> = {
  needs_input: "warn",
  finished: "accent",
  setup_failed: "bad",
  create_failed: "bad",
  ci_failing: "bad",
  changes_requested: "warn",
  merged_cleanup: "merged",
  ready_to_merge: "ok",
};

/** Sessions needing the user, most urgent first. */
export function needingAttention(sessions: Session[], seen: Record<string, string>) {
  const rank: Record<Reason, number> = {
    needs_input: 0,
    create_failed: 1,
    setup_failed: 2,
    ci_failing: 3,
    changes_requested: 4,
    finished: 5,
    ready_to_merge: 6,
    merged_cleanup: 7,
  };
  return sessions
    .map((s) => ({ session: s, reason: attentionOf(s, seen) }))
    .filter((x): x is { session: Session; reason: Reason } => !!x.reason)
    .sort((a, b) => rank[a.reason] - rank[b.reason] || a.session.name.localeCompare(b.session.name));
}
