// Sessions on their way out — cleaned up or removed from this app — until
// the listing no longer has them: their rows say so the moment it's asked,
// not once the cloud has caught up (seconds, for a VM to go).
import { create } from "zustand";

export const useRemoving = create<{ ids: Record<string, string> }>(() => ({ ids: {} }));

/** Marks a session going, with what to say meanwhile ("Cleaning up…"). */
export function markRemoving(id: string, doing: string) {
  useRemoving.setState((s) => ({ ids: { ...s.ids, [id]: doing } }));
}

/** It isn't going after all (the removal failed), or it's gone. */
export function unmarkRemoving(id: string) {
  useRemoving.setState((s) => {
    const { [id]: _, ...rest } = s.ids;
    return { ids: rest };
  });
}

/** Forgets the ones the listing no longer has. */
export function settleRemoving(listed: Set<string>) {
  const ids = useRemoving.getState().ids;
  for (const id of Object.keys(ids)) if (!listed.has(id)) unmarkRemoving(id);
}
