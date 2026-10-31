// System notifications. In the app they go through the native bridge, whose
// clicks come back as "notification-clicked"; elsewhere (development in a
// browser, an unbundled build) through the web's own.
import { invoke } from "@tauri-apps/api/core";
import { listen } from "@tauri-apps/api/event";
import { inTauri } from "@/api/transport";

let onWebClick: ((sessionId: string) => void) | null = null;

export async function showNotification(n: { id: string; title: string; body: string; sessionId: string }) {
  if (inTauri) {
    const ok = await invoke<boolean>("notify", {
      id: n.id,
      title: n.title,
      body: n.body,
      session: n.sessionId,
    }).catch(() => false);
    if (ok) return;
  }
  if (!("Notification" in window)) return;
  if (Notification.permission === "default") await Notification.requestPermission();
  if (Notification.permission !== "granted") return;
  const w = new Notification(n.title, { body: n.body, tag: n.id });
  w.onclick = () => {
    window.focus();
    onWebClick?.(n.sessionId);
  };
}

/** Calls fn with the session of a clicked notification. */
export function onNotificationClick(fn: (sessionId: string) => void): () => void {
  onWebClick = fn;
  if (!inTauri) return () => (onWebClick = null);
  const un = listen<string>("notification-clicked", (e) => fn(e.payload));
  return () => {
    onWebClick = null;
    void un.then((f) => f());
  };
}
