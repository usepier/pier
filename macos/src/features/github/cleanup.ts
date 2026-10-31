import { rpc } from "@/api/client";
import type { Session } from "@/api/protocol.gen";
import { confirm } from "@/lib/confirm";
import { markRemoving, unmarkRemoving } from "@/state/removing";
import { attempt, toast } from "@/state/toasts";

/**
 * Cleans up a session whose pull request merged: once confirmed, its VM
 * and disk go, so it costs nothing more. pier refuses while anything in
 * it is left behind (uncommitted, or not what merged). The session shows
 * as cleaning up from the moment it's confirmed (onStart: e.g. leave its
 * page); true once it's gone.
 */
export async function cleanUp(s: Session, onStart?: () => void): Promise<boolean> {
  const sure = await confirm(
    `Clean up ${s.name}?`,
    "Its pull request merged. The VM and its disk go, so it stops costing anything.",
    { ok: "Clean up", destructive: true },
  );
  if (!sure) return false;
  markRemoving(s.id, "Cleaning up…");
  onStart?.();
  const ok = await attempt(() => rpc.call("sessions.removeMerged", { id: s.id }));
  if (ok === undefined) {
    unmarkRemoving(s.id);
    return false;
  }
  toast(`Cleaned up ${s.name}`, "ok");
  return true;
}
