// App preferences that live with the app, not in pier's config: what
// Return does, whether a message sent mid-turn steers or waits, terminals,
// diffs, cleanup.
import { create } from "zustand";
import { persist } from "zustand/middleware";

export interface Prefs {
  /** A hidden terminal tab detaches after this long (it keeps the session awake while attached). */
  hiddenDetachMs: number;
  /** Every terminal detaches once the app has been in the background this long. */
  backgroundDetachMs: number;
  terminalFontSize: number;
  /** Diffs side by side rather than one column. */
  diffSplit: boolean;
  /** Remove sessions whose pull request merged, when nothing in them is left behind. */
  autoRemoveMerged: boolean;
  /** Start the proxy (sessions as <session>.pier) when the app opens. */
  proxyOnLaunch: boolean;
  /** enter: Return sends, ⇧Return breaks the line; cmdEnter: ⌘Return sends. */
  sendKey: "enter" | "cmdEnter";
  /** steer: a message sent mid-turn goes into the turn (⌘⇧Return queues);
   * queue: it waits for the turn to end (⌘⇧Return steers). */
  followUp: "steer" | "queue";
  /** Tool calls stay in view instead of folding into a summary line. */
  expandTools: boolean;
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
      proxyOnLaunch: false,
      sendKey: "enter",
      followUp: "steer",
      expandTools: false,
      set: (p) => set(p),
    }),
    { name: "pier.prefs" },
  ),
);
