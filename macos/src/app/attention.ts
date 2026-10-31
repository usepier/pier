// Getting the user's attention: notifications for what pier reports
// (a finished turn, an agent waiting, a failed setup), the Dock badge
// counting what needs them, and prompts still on their way going out
// whenever pier is (back) up.
import { getCurrentWindow } from "@tauri-apps/api/window";
import { useEffect } from "react";
import { rpc } from "@/api/client";
import { useStatus } from "@/api/live";
import { inTauri } from "@/api/transport";
import { onNotificationClick, showNotification } from "@/lib/notify";
import { needingAttention, useSeen } from "@/state/attention";
import { flushOutbox } from "@/state/outbox";
import { useOverview } from "@/state/overview";
import { usePrefs } from "@/state/prefs";
import { toast } from "@/state/toasts";
import { current } from "./appCommands";
import { router } from "./router";

const triedRemoving = new Set<string>();

export function useAttentionEffects() {
  const status = useStatus();
  useEffect(() => {
    if (status === "ready") flushOutbox();
  }, [status]);

  useEffect(() => {
    const off = rpc.on("notify", (n) => {
      // Not about what you're looking at.
      if (document.hasFocus() && !document.hidden && current().session === n.sessionId) return;
      void showNotification({ id: n.id, title: n.title, body: n.body, sessionId: n.sessionId });
    });
    const offClick = onNotificationClick(
      (id) => void router.navigate({ to: "/session/$id", params: { id } }),
    );
    return () => {
      off();
      offClick();
    };
  }, []);

  const o = useOverview();
  const seen = useSeen((s) => s.seen);

  // Merged sessions go, if asked — pier checks nothing would be lost, and
  // says no otherwise; each is tried once per run of the app.
  const autoRemove = usePrefs((p) => p.autoRemoveMerged);
  useEffect(() => {
    if (!autoRemove || !o) return;
    for (const s of o.sessions) {
      if (s.attention !== "merged_cleanup" || triedRemoving.has(s.id)) continue;
      triedRemoving.add(s.id);
      void rpc
        .call("sessions.removeMerged", { id: s.id })
        .then(() => toast(`Removed ${s.name}: its pull request merged`, "ok"))
        .catch(() => {});
    }
  }, [autoRemove, o]);
  const count = o ? needingAttention(o.sessions, seen).length : 0;
  useEffect(() => {
    if (!inTauri) return;
    void getCurrentWindow()
      .setBadgeCount(count > 0 ? count : undefined)
      .catch(() => {});
  }, [count]);
}
