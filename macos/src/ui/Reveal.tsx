import { type ReactNode, useState } from "react";

/**
 * Content that slides open and shut, its height animating from nothing
 * to what it needs. It mounts on first open and unmounts once shut, so a
 * long transcript doesn't carry every closed block's content.
 */
export function Reveal({ open, children }: { open: boolean; children: ReactNode }) {
  const [shown, setShown] = useState(open);
  if (open && !shown) setShown(true);
  return (
    <div
      className={`grid transition-[grid-template-rows,opacity] duration-200 ease-out motion-reduce:transition-none ${open ? "grid-rows-[1fr] opacity-100" : "grid-rows-[0fr] opacity-0"}`}
      onTransitionEnd={(e) => {
        if (e.target === e.currentTarget && !open) setShown(false);
      }}
    >
      <div className="min-h-0 overflow-hidden">{shown && children}</div>
    </div>
  );
}
