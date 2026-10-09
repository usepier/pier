// Review comments on a session's diffs, gathered until they go to the
// agent as one message.
import { create } from "zustand";
import { persist } from "zustand/middleware";

export interface Comment {
  id: string;
  path: string;
  side: "additions" | "deletions";
  /** The line (and the last line of a range) on that side. */
  line: number;
  endLine?: number;
  text: string;
  /** The lines it is about, quoted in the message. */
  code?: string;
}

interface Comments {
  comments: Record<string, Comment[]>;
  add: (sessionId: string, c: Comment) => void;
  update: (sessionId: string, id: string, text: string) => void;
  remove: (sessionId: string, id: string) => void;
  clear: (sessionId: string) => void;
}

export const useComments = create<Comments>()(
  persist(
    (set) => ({
      comments: {},
      add: (sid, c) => set((s) => ({ comments: { ...s.comments, [sid]: [...(s.comments[sid] ?? []), c] } })),
      update: (sid, id, text) =>
        set((s) => ({
          comments: {
            ...s.comments,
            [sid]: (s.comments[sid] ?? []).map((c) => (c.id === id ? { ...c, text } : c)),
          },
        })),
      remove: (sid, id) =>
        set((s) => ({
          comments: { ...s.comments, [sid]: (s.comments[sid] ?? []).filter((c) => c.id !== id) },
        })),
      clear: (sid) =>
        set((s) => {
          const { [sid]: _, ...rest } = s.comments;
          return { comments: rest };
        }),
    }),
    { name: "pier.comments" },
  ),
);

/** What heads review comments unless the user's prompts say otherwise. */
export const reviewHeader = "Review comments on your changes — please address each one:";

/** The comments as one message for the agent, file by file. */
export function reviewMessage(comments: Comment[], header = reviewHeader): string {
  const byPath = new Map<string, Comment[]>();
  for (const c of comments) byPath.set(c.path, [...(byPath.get(c.path) ?? []), c]);
  const parts = [header.trim()];
  for (const [path, list] of byPath) {
    for (const c of list.sort((a, b) => a.line - b.line)) {
      const where = c.endLine && c.endLine !== c.line ? `${c.line}-${c.endLine}` : `${c.line}`;
      const old = c.side === "deletions" ? " (removed lines)" : "";
      let block = `\n${path}:${where}${old}`;
      if (c.code)
        block += `\n${c.code
          .split("\n")
          .map((l) => `> ${l}`)
          .join("\n")}`;
      block += `\n${c.text.trim()}`;
      parts.push(block);
    }
  }
  return parts.join("\n");
}
