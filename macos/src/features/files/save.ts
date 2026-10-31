// ⌘S saves the editor in view: the visible editor registers its save here.
let active: (() => void) | null = null;

/** Makes fn what ⌘S runs; the returned function takes it back. */
export function setActiveSave(fn: () => void): () => void {
  active = fn;
  return () => {
    if (active === fn) active = null;
  };
}

export function saveActive(): boolean {
  if (!active) return false;
  active();
  return true;
}

export function hasActiveSave(): boolean {
  return active !== null;
}
