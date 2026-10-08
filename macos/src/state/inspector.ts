// The session inspector's tab, and requests to show its search.
import { create } from "zustand";
import { persist } from "zustand/middleware";

export type InspectorTab = "changes" | "pr" | "files" | "env" | "log" | "details";

interface Inspector {
  tab: InspectorTab;
  /** The Files tab lists files, or searches their contents. */
  filesMode: "tree" | "search";
  /** Bumped to focus the search field. */
  focusSearch: number;
  setTab: (t: InspectorTab) => void;
  setFilesMode: (m: "tree" | "search") => void;
  showSearch: () => void;
}

export const useInspector = create<Inspector>()(
  persist(
    (set) => ({
      tab: "changes",
      filesMode: "tree",
      focusSearch: 0,
      setTab: (tab) => set({ tab }),
      setFilesMode: (filesMode) => set({ filesMode }),
      showSearch: () => set((s) => ({ tab: "files", filesMode: "search", focusSearch: s.focusSearch + 1 })),
    }),
    { name: "pier.inspector", partialize: (s) => ({ tab: s.tab, filesMode: s.filesMode }) },
  ),
);
