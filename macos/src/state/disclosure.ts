// What's open in a transcript — step blocks, tool calls, thoughts — by
// item id. Rows come and go (virtualized, regrouped as steps arrive), so
// it lives here rather than in them: new output never closes what you
// opened, and scrolling back finds it as you left it.
import { useLayoutEffect } from "react";
import { create } from "zustand";

const useDisclosure = create<{ open: Record<string, boolean> }>(() => ({ open: {} }));

/** Whether id is open (initially as given), and a setter. */
export function useOpen(id: string, initial = false): [boolean, (open: boolean) => void] {
  const v = useDisclosure((s) => s.open[id]);
  const set = (open: boolean) => useDisclosure.setState((s) => ({ open: { ...s.open, [id]: open } }));
  // Opened from the start, it stays so: the initial guess can change later.
  useLayoutEffect(() => {
    if (initial && useDisclosure.getState().open[id] === undefined) set(true);
  });
  return [v ?? initial, set];
}

/** Whether id was opened, outside a component. */
export function isOpen(id: string): boolean {
  return !!useDisclosure.getState().open[id];
}
