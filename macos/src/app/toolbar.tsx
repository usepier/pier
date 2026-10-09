// The window's toolbar band: a page puts its title and actions there
// (<Toolbar>), next to the traffic lights' row, instead of spending a
// header row of its own.
import { createContext, type ReactNode, useContext } from "react";
import { createPortal } from "react-dom";

export const ToolbarSlot = createContext<HTMLElement | null>(null);

/** Renders its children in the window's toolbar band. */
export function Toolbar({ children }: { children: ReactNode }) {
  const slot = useContext(ToolbarSlot);
  return slot ? createPortal(children, slot) : null;
}
