import { useRouterState } from "@tanstack/react-router";
import { useEffect } from "react";
import { rpc } from "@/api/client";
import { useStatus } from "@/api/live";
import { AccountBanner } from "@/features/auth/AccountBanner";
import { QuickOpen } from "@/features/files/QuickOpen";
import { NewSessionSheet } from "@/features/newSession/NewSessionSheet";
import { Palette } from "@/features/palette/Palette";
import { Sidebar } from "@/features/sidebar/Sidebar";
import { applyAccent } from "@/lib/theme";
import { useJobsBootstrap } from "@/state/jobs";
import { useActivityReporting, useLoadRecorder } from "@/state/overview";
import { usePrefs } from "@/state/prefs";
import { startProxy } from "@/state/proxy";
import { Toasts } from "@/ui/Toasts";
import { useAppCommands } from "./appCommands";
import { useAttentionEffects } from "./attention";
import { useDeepLinks } from "./deepLinks";
import { router } from "./router";
import { Shell } from "./Shell";
import { UpdateBanner } from "./UpdateBanner";

let sentToSetup = false;

export function Root() {
  useAppCommands();
  useActivityReporting();
  useLoadRecorder();
  useJobsBootstrap();
  useAttentionEffects();
  useDeepLinks();
  const status = useStatus();
  // A Mac pier hasn't been set up on opens on setup, once.
  useEffect(() => {
    if (status !== "ready" || rpc.info?.configured || sentToSetup) return;
    sentToSetup = true;
    void router.navigate({ to: "/setup" });
  }, [status]);
  const proxyOnLaunch = usePrefs((p) => p.proxyOnLaunch);
  useEffect(() => {
    if (status === "ready" && proxyOnLaunch && rpc.info?.configured) void startProxy();
  }, [status, proxyOnLaunch]);
  useEffect(() => {
    if (status !== "ready") return;
    void rpc
      .call("settings.get", {})
      .then((s) => applyAccent(s.settings.find((x) => x.key === "theme")?.value))
      .catch(() => applyAccent(undefined));
  }, [status]);
  useEffect(() => {
    // The web view would reload on ⌘R; that is never what anyone wants here.
    const block = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "r") e.preventDefault();
    };
    window.addEventListener("keydown", block);
    return () => window.removeEventListener("keydown", block);
  }, []);
  // Settings take the whole window, with their own sidebar.
  const fullWindow = useRouterState({ select: (r) => r.location.pathname.startsWith("/settings") });
  return (
    <>
      <Shell
        fullWindow={fullWindow}
        sidebar={<Sidebar />}
        banners={
          <>
            <UpdateBanner />
            <AccountBanner />
          </>
        }
      />
      <NewSessionSheet />
      <QuickOpen />
      <Palette />
      <Toasts />
    </>
  );
}
