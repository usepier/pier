// App preferences that live with the app, not in pier's config.
import { create } from "zustand";
import { persist } from "zustand/middleware";

interface Prefs {
  /** A hidden terminal tab detaches after this long (it keeps the session awake while attached). */
  hiddenDetachMs: number;
  /** Every terminal detaches once the app has been in the background this long. */
  backgroundDetachMs: number;
  terminalFontSize: number;
  /** Diffs side by side rather than one column. */
  diffSplit: boolean;
  /** Remove sessions whose pull request merged, when nothing in them is left behind. */
  autoRemoveMerged: boolean;
  set: (p: Partial<Omit<Prefs, "set">>) => void;
}

export const usePrefs = create<Prefs>()(
  persist(
    (set) => ({
      hiddenDetachMs: 60_000,
      backgroundDetachMs: 600_000,
      terminalFontSize: 12.5,
      diffSplit: false,
      autoRemoveMerged: false,
      set: (p) => set(p),
    }),
    { name: "pier.prefs" },
  ),
);
