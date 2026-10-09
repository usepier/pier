// What a session's state allows, in one place.
import type { Session } from "@/api/protocol.gen";

/** The session's VM is up, whatever its supervisor says it's doing. */
export function running(s: Session): boolean {
  return s.state === "running" || s.state === "working" || s.state === "idle";
}

/** Its checkout can be read from this Mac now (the VM is up and its key is here). */
export function readable(s: Session): boolean {
  return running(s) && s.hasKey;
}

/** It costs by the hour: starting or up. */
export function billed(s: Session): boolean {
  return s.state === "creating" || running(s);
}
