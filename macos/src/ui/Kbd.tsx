import type { ReactNode } from "react";

/** A key, as shortcuts name it: ⌘, ⏎. */
export function Kbd({ children }: { children: ReactNode }) {
  return (
    <kbd className="inline-flex h-[17px] min-w-[17px] items-center justify-center rounded-[4px] border border-strong px-1 font-sans text-2xs text-tertiary">
      {children}
    </kbd>
  );
}
