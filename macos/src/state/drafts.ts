// What you were typing to each session's agent, kept across views and
// relaunches.
import { create } from "zustand";
import { persist } from "zustand/middleware";

interface Drafts {
  drafts: Record<string, string>;
  setDraft: (sessionId: string, text: string) => void;
}

export const useDrafts = create<Drafts>()(
  persist(
    (set) => ({
      drafts: {},
      setDraft: (sessionId, text) =>
        set((s) => {
          const { [sessionId]: _, ...rest } = s.drafts;
          return { drafts: text ? { ...rest, [sessionId]: text } : rest };
        }),
    }),
    { name: "pier.drafts" },
  ),
);
