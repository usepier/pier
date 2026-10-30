// Chats opened in the app but not yet started: a tab with a composer and
// the agent's pickers, until its first prompt starts it in the session.
import { create } from "zustand";
import { persist } from "zustand/middleware";

export interface NewChat {
  id: string;
  agent: string;
  model: string;
  effort: string;
  mode: string;
  fast?: boolean;
}

interface NewChats {
  chats: Record<string, NewChat[]>;
  open: (sessionId: string, c: NewChat) => void;
  update: (sessionId: string, id: string, p: Partial<NewChat>) => void;
  drop: (sessionId: string, id: string) => void;
}

const none: NewChat[] = [];

export const useNewChats = create<NewChats>()(
  persist(
    (set) => ({
      chats: {},
      open: (sessionId, c) =>
        set((s) => ({ chats: { ...s.chats, [sessionId]: [...(s.chats[sessionId] ?? none), c] } })),
      update: (sessionId, id, p) =>
        set((s) => ({
          chats: {
            ...s.chats,
            [sessionId]: (s.chats[sessionId] ?? none).map((c) => (c.id === id ? { ...c, ...p } : c)),
          },
        })),
      drop: (sessionId, id) =>
        set((s) => ({
          chats: { ...s.chats, [sessionId]: (s.chats[sessionId] ?? none).filter((c) => c.id !== id) },
        })),
    }),
    { name: "pier.newChats" },
  ),
);

export function useNewChatsOf(sessionId: string): NewChat[] {
  return useNewChats((s) => s.chats[sessionId] ?? none);
}
