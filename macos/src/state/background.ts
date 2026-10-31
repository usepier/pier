// How long the app has been out of sight: terminals detach after a while in
// the background so the sessions they show can park.
import { useEffect, useState } from "react";
import { usePrefs } from "./prefs";

let since: number | null = null;
const listeners = new Set<() => void>();

function update() {
  const away = document.hidden || !document.hasFocus();
  const next = away ? (since ?? Date.now()) : null;
  if (next !== since) {
    since = next;
    for (const l of listeners) l();
  }
}

if (typeof window !== "undefined") {
  window.addEventListener("focus", update);
  window.addEventListener("blur", update);
  document.addEventListener("visibilitychange", update);
}

/** True once the app has been in the background longer than the preference allows. */
export function useBackgroundExpired(): boolean {
  const limit = usePrefs((p) => p.backgroundDetachMs);
  const [expired, setExpired] = useState(false);
  useEffect(() => {
    let timer: ReturnType<typeof setTimeout> | undefined;
    const check = () => {
      clearTimeout(timer);
      if (since === null) {
        setExpired(false);
        return;
      }
      const left = since + limit - Date.now();
      if (left <= 0) setExpired(true);
      else timer = setTimeout(check, left);
    };
    listeners.add(check);
    check();
    return () => {
      listeners.delete(check);
      clearTimeout(timer);
    };
  }, [limit]);
  return expired;
}

/** True while the window is frontmost and visible. */
export function useFocused(): boolean {
  const [focused, setFocused] = useState(() => since === null);
  useEffect(() => {
    const fn = () => setFocused(since === null);
    listeners.add(fn);
    update();
    fn();
    return () => {
      listeners.delete(fn);
    };
  }, []);
  return focused;
}
