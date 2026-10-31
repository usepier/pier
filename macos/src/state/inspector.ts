// The session inspector's tab, and requests to show its search.
import { create } from "zustand";
import { persist } from "zustand/middleware";
import { focusTarget } from "@/lib/focus";

export type InspectorTab = "changes" | "pr" | "files" | "env" | "log" | "details";

interface Inspector {
  tab: InspectorTab;
  /** The Files tab lists files, or searches their contents. */
  filesMode: "tree" | "search";
  setTab: (t: InspectorTab) => void;
  setFilesMode: (m: "tree" | "search") => void;
  showSearch: () => void;
}

export const useInspector = create<Inspector>()(
  persist(
    (set) => ({
      tab: "changes",
      filesMode: "tree",
      setTab: (tab) => set({ tab }),
      setFilesMode: (filesMode) => set({ filesMode }),
      showSearch: () => {
        set({ tab: "files", filesMode: "search" });
        focusTarget("search");
      },
    }),
    { name: "pier.inspector", partialize: (s) => ({ tab: s.tab, filesMode: s.filesMode }) },
  ),
);
