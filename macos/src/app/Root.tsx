import { useEffect } from "react";
import { rpc } from "@/api/client";
import { useStatus } from "@/api/live";
import { AccountBanner } from "@/features/auth/AccountBanner";
import { NewSessionSheet } from "@/features/newSession/NewSessionSheet";
import { Sidebar } from "@/features/sidebar/Sidebar";
import { applyAccent } from "@/lib/theme";
import { useJobsBootstrap } from "@/state/jobs";
import { useActivityReporting, useLoadRecorder } from "@/state/overview";
import { Toasts } from "@/ui/Toasts";
import { useAppCommands } from "./appCommands";
import { Shell } from "./Shell";

export function Root() {
  useAppCommands();
  useActivityReporting();
  useLoadRecorder();
  useJobsBootstrap();
  const status = useStatus();
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
  return (
    <>
      <Shell sidebar={<Sidebar />} banners={<AccountBanner />} />
      <NewSessionSheet />
      <Toasts />
    </>
  );
}
