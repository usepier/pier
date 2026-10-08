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
import { current } from "./appCommands";
import { router } from "./router";

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
  const count = o ? needingAttention(o.sessions, seen).length : 0;
  useEffect(() => {
    if (!inTauri) return;
    void getCurrentWindow()
      .setBadgeCount(count > 0 ? count : undefined)
      .catch(() => {});
  }, [count]);
}
