import { useEffect } from "react";
import { inTauri } from "@/api/transport";
import { allCommands, matchesShortcut, registerCommands, runCommand, useCommandsVersion } from "./commands";
import { buildMenu } from "./menu";
import { router } from "./router";

/** The commands every view has, the menu bar they feed, and the browser shortcut fallback. */
export function useAppCommands() {
  useEffect(
    () =>
      registerCommands([
        {
          id: "app.settings",
          title: "Settings…",
          shortcut: "CmdOrCtrl+,",
          menu: "app",
          run: () => router.navigate({ to: "/settings" }),
        },
        {
          id: "go.home",
          title: "Home",
          shortcut: "CmdOrCtrl+Shift+H",
          menu: "go",
          run: () => router.navigate({ to: "/" }),
        },
        {
          id: "go.back",
          title: "Back",
          shortcut: "CmdOrCtrl+[",
          menu: "go",
          run: () => router.history.back(),
        },
        {
          id: "go.forward",
          title: "Forward",
          shortcut: "CmdOrCtrl+]",
          menu: "go",
          run: () => router.history.forward(),
        },
      ]),
    [],
  );
  const version = useCommandsVersion();
  useEffect(() => {
    void version;
    void buildMenu();
  }, [version]);
  useEffect(() => {
    if (inTauri) return; // the native menu owns shortcuts
    const onKey = (e: KeyboardEvent) => {
      for (const c of allCommands()) {
        if (c.shortcut && matchesShortcut(e, c.shortcut)) {
          e.preventDefault();
          runCommand(c.id);
          return;
        }
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);
}
