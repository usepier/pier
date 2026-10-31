import { useNavigate, useRouterState } from "@tanstack/react-router";
import { useEffect } from "react";
import { rpc } from "@/api/client";
import { useStatus } from "@/api/live";
import { AccountBanner } from "@/features/auth/AccountBanner";
import { QuickOpen } from "@/features/files/QuickOpen";
import { Palette } from "@/features/palette/Palette";
import { Sidebar } from "@/features/sidebar/Sidebar";
import { applyAccent } from "@/lib/theme";
import { useForgetGone } from "@/state/forget";
import { useJobsBootstrap } from "@/state/jobs";
import { useActivityReporting } from "@/state/overview";
import { usePrefs } from "@/state/prefs";
import { startProxy } from "@/state/proxy";
import { toast } from "@/state/toasts";
import { useUI } from "@/state/ui";
import { ConfirmHost } from "@/ui/ConfirmHost";
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
  useForgetGone();
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
  // The app moved since the pier command went in: put the link right,
  // when that needs no password; else say where to.
  useEffect(() => {
    if (status !== "ready") return;
    void (async () => {
      const st = await rpc.call("cli.status", {}).catch(() => undefined);
      if (!st?.stale) return;
      const after = await rpc.call("cli.install", { quiet: true }).catch(() => undefined);
      toast(
        after?.installed
          ? "The pier command follows the app to where it is now"
          : "The pier command points at where the app was — repair it in Settings › Command line",
        after?.installed ? "ok" : "info",
      );
    })();
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
      <NewSessionOpener />
      <QuickOpen />
      <Palette />
      <Toasts />
      <ConfirmHost />
    </>
  );
}

/** New Session is a page: whatever asks for it (a button, ⌘N, a link) goes there. */
function NewSessionOpener() {
  const req = useUI((s) => s.newSession);
  const close = useUI((s) => s.closeNewSession);
  const navigate = useNavigate();
  useEffect(() => {
    if (!req.open) return;
    close();
    navigate({
      to: "/new",
      search: { repo: req.repo, branch: req.preset?.branch, prompt: req.preset?.prompt },
    });
  }, [req, close, navigate]);
  return null;
}
