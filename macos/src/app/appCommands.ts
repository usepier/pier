import { useEffect } from "react";
import { rpc } from "@/api/client";
import { inTauri } from "@/api/transport";
import { linkCheckout } from "@/features/repos/link";
import { confirm } from "@/lib/confirm";
import { trackJob } from "@/state/jobs";
import { attempt, toast } from "@/state/toasts";
import { useUI } from "@/state/ui";
import { allCommands, matchesShortcut, registerCommands, runCommand, useCommandsVersion } from "./commands";
import { buildMenu } from "./menu";
import { router } from "./router";

/** The session or repo the window shows, from the route. */
export function current(): { session?: string; repo?: string } {
  const p = router.state.location.pathname;
  const m = p.match(/^\/(session|repo)\/([^/]+)/);
  if (!m) return {};
  const v = decodeURIComponent(m[2]!);
  return m[1] === "session" ? { session: v } : { repo: v };
}

async function overview() {
  return rpc.call("overview.get", {});
}

async function currentSession() {
  const id = current().session;
  if (!id) return undefined;
  return (await overview()).sessions.find((s) => s.id === id);
}

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
          id: "file.newSession",
          title: "New Session…",
          shortcut: "CmdOrCtrl+N",
          menu: "file",
          run: () => useUI.getState().openNewSession(current().repo),
        },
        {
          id: "file.link",
          title: "Link Repository…",
          shortcut: "CmdOrCtrl+Shift+O",
          menu: "file",
          run: () => void linkCheckout(),
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
        {
          id: "session.wake",
          title: "Wake",
          menu: "session",
          keywords: "resume start",
          enabled: () => !!current().session,
          run: async () => {
            const s = await currentSession();
            if (s?.state === "parked") await attempt(() => rpc.call("sessions.resume", { id: s.id }));
          },
        },
        {
          id: "session.keep",
          title: "Keep Awake",
          menu: "session",
          keywords: "pin never park",
          enabled: () => !!current().session,
          run: async () => {
            const s = await currentSession();
            if (!s) return;
            const kept = await attempt(async () => {
              await rpc.call("sessions.keep", { id: s.id });
              return true;
            });
            if (kept) toast(`${s.name} won't park when idle`, "ok");
          },
        },
        {
          id: "session.remove",
          title: "Remove…",
          menu: "session",
          keywords: "delete destroy",
          enabled: () => !!current().session,
          run: async () => {
            const s = await currentSession();
            if (!s) return;
            if (
              !(await confirm(
                `Remove ${s.name}?`,
                "The VM and its disk are destroyed. Anything not pushed is lost.",
                { ok: "Remove", destructive: true },
              ))
            )
              return;
            await attempt(() => rpc.call("sessions.remove", { id: s.id }));
            router.navigate({ to: "/repo/$name", params: { name: s.repo } });
          },
        },
        {
          id: "repo.bake",
          title: "Bake Session Image",
          menu: "repo",
          enabled: () => !!current().repo,
          run: async () => {
            const name = current().repo;
            const r = (await overview()).repos.find((x) => x.name === name);
            if (!r?.checkout) return toast(`Locate your clone of ${name} first`, "info");
            const j = await attempt(() => rpc.call("image.bake", { path: r.checkout!.path }));
            if (j) trackJob(j);
          },
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
