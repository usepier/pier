// What a session's center shows: its chat, its terminals, and the diffs
// and files opened from the inspector — each a tab, the active one
// remembered per session.
import { create } from "zustand";
import { persist } from "zustand/middleware";

export interface Doc {
  /** "diff:<path>" or "file:<path>". */
  id: string;
  kind: "diff" | "file";
  path: string;
  oldPath?: string;
  status?: string;
  /** A line to show when opened (a search hit). */
  line?: number;
}

interface Docs {
  docs: Record<string, Doc[]>;
  /** The center's active tab per session: "chat", a window id, or a doc id. */
  active: Record<string, string>;
  /** Bumped to ask the editor of a doc to reveal its line again. */
  reveal: number;
  open: (sessionId: string, doc: Omit<Doc, "id">) => void;
  close: (sessionId: string, id: string) => void;
  setActive: (sessionId: string, id: string) => void;
}

export const docId = (kind: Doc["kind"], path: string) => `${kind}:${path}`;

export const useDocs = create<Docs>()(
  persist(
    (set) => ({
      docs: {},
      active: {},
      reveal: 0,
      open: (sessionId, d) =>
        set((s) => {
          const id = docId(d.kind, d.path);
          const list = s.docs[sessionId] ?? [];
          const doc = { ...d, id };
          const docs = list.some((x) => x.id === id)
            ? list.map((x) => (x.id === id ? doc : x))
            : [...list, doc];
          return {
            docs: { ...s.docs, [sessionId]: docs },
            active: { ...s.active, [sessionId]: id },
            reveal: s.reveal + 1,
          };
        }),
      close: (sessionId, id) =>
        set((s) => {
          const list = s.docs[sessionId] ?? [];
          const at = list.findIndex((x) => x.id === id);
          const rest = list.filter((x) => x.id !== id);
          const active = { ...s.active };
          if (active[sessionId] === id) active[sessionId] = rest[Math.min(at, rest.length - 1)]?.id ?? "chat";
          return { docs: { ...s.docs, [sessionId]: rest }, active };
        }),
      setActive: (sessionId, id) => set((s) => ({ active: { ...s.active, [sessionId]: id } })),
    }),
    { name: "pier.docs", partialize: (s) => ({ docs: s.docs, active: s.active }) },
  ),
);
