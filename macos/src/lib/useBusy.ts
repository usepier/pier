import { useRef, useState } from "react";

/**
 * An action that says while it runs, and runs once at a time: busy for a
 * button's isPending, run to start it (a press while it runs does nothing).
 */
export function useBusy(): [boolean, <T>(action: () => Promise<T>) => Promise<T | undefined>] {
  const [busy, setBusy] = useState(false);
  const running = useRef(false);
  const run = async <T>(action: () => Promise<T>) => {
    if (running.current) return undefined;
    running.current = true;
    setBusy(true);
    try {
      return await action();
    } finally {
      running.current = false;
      setBusy(false);
    }
  };
  return [busy, run];
}
