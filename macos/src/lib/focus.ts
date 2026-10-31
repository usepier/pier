// Where focus goes when a command asks for it: each pane registers its main
// control under a name — "composer", "search" — while it is on screen, and
// a command asks for one by name. A control that isn't there yet (its tab
// just opened) takes the focus as it registers, if it does so soon.
import { type RefObject, useEffect } from "react";

export type FocusName = "composer" | "search";

const targets = new Map<FocusName, () => void>();
let pending: { name: FocusName; until: number } | null = null;

/**
 * Puts the focus on the named control, now or as soon as it appears; next
 * waits for the one about to appear (a tab just opened replaces the one
 * on screen).
 */
export function focusTarget(name: FocusName, opts: { next?: boolean } = {}) {
  const focus = opts.next ? undefined : targets.get(name);
  if (focus) {
    pending = null;
    focus();
    return;
  }
  pending = { name, until: Date.now() + 2000 };
}

/** Registers a control under name while mounted (and while enabled). */
export function useFocusTarget(
  name: FocusName,
  ref: RefObject<HTMLElement | null>,
  opts: { enabled?: boolean; select?: boolean } = {},
) {
  const enabled = opts.enabled ?? true;
  const select = opts.select ?? false;
  useEffect(() => {
    if (!enabled) return;
    const focus = () => {
      const el = ref.current;
      if (!el) return;
      el.focus();
      if (select && (el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement)) el.select();
    };
    targets.set(name, focus);
    if (pending?.name === name) {
      if (Date.now() < pending.until) focus();
      pending = null;
    }
    return () => {
      if (targets.get(name) === focus) targets.delete(name);
    };
  }, [name, ref, enabled, select]);
}
