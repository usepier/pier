// UI state that outlives a view: sheets, collapsed repos.
import { create } from "zustand";
import { persist } from "zustand/middleware";

interface UI {
  newSession: { open: boolean; repo?: string };
  openNewSession: (repo?: string) => void;
  closeNewSession: () => void;
  collapsed: Record<string, boolean>;
  toggleRepo: (name: string) => void;
}

export const useUI = create<UI>()(
  persist(
    (set) => ({
      newSession: { open: false },
      openNewSession: (repo) => set({ newSession: { open: true, repo } }),
      closeNewSession: () => set({ newSession: { open: false } }),
      collapsed: {},
      toggleRepo: (name) => set((s) => ({ collapsed: { ...s.collapsed, [name]: !s.collapsed[name] } })),
    }),
    { name: "pier.ui", partialize: (s) => ({ collapsed: s.collapsed }) },
  ),
);
