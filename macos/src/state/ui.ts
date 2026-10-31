// UI state that outlives a view: sheets, collapsed repos.
import { create } from "zustand";
import { persist } from "zustand/middleware";

/** What the New Session page opens with. */
export interface NewSessionPreset {
  branch?: string;
  prompt?: string;
}

interface UI {
  newSession: { open: boolean; repo?: string; preset?: NewSessionPreset };
  openNewSession: (repo?: string, preset?: NewSessionPreset) => void;
  closeNewSession: () => void;
  collapsed: Record<string, boolean>;
  toggleRepo: (name: string) => void;
}

export const useUI = create<UI>()(
  persist(
    (set) => ({
      newSession: { open: false },
      openNewSession: (repo, preset) => set({ newSession: { open: true, repo, preset } }),
      closeNewSession: () => set({ newSession: { open: false } }),
      collapsed: {},
      toggleRepo: (name) => set((s) => ({ collapsed: { ...s.collapsed, [name]: !s.collapsed[name] } })),
    }),
    { name: "pier.ui", partialize: (s) => ({ collapsed: s.collapsed }) },
  ),
);
