// pier:// links open the app where they point:
//   pier://session/<id>      a session
//   pier://repo/<name>       a repository's cockpit
//   pier://new?repo=<name>&branch=<b>&prompt=<text>   New Session, filled in
import { getCurrent, onOpenUrl } from "@tauri-apps/plugin-deep-link";
import { useEffect } from "react";
import { inTauri } from "@/api/transport";
import { useUI } from "@/state/ui";
import { router } from "./router";

export type Link =
  | { to: "session"; id: string }
  | { to: "repo"; name: string }
  | { to: "new"; repo?: string; branch?: string; prompt?: string };

/** Reads a pier:// link; undefined for one this app doesn't know. */
export function parseLink(raw: string): Link | undefined {
  let u: URL;
  try {
    u = new URL(raw);
  } catch {
    return undefined;
  }
  if (u.protocol !== "pier:") return undefined;
  // pier://session/x parses with host "session" and path "/x".
  const parts = [u.hostname, ...u.pathname.split("/")].filter(Boolean).map(decodeURIComponent);
  const q = (k: string) => u.searchParams.get(k) ?? undefined;
  switch (parts[0]) {
    case "session":
      return parts[1] ? { to: "session", id: parts[1] } : undefined;
    case "repo":
      return parts[1] ? { to: "repo", name: parts[1] } : undefined;
    case "new":
      return { to: "new", repo: q("repo"), branch: q("branch"), prompt: q("prompt") };
  }
  return undefined;
}

export function followLink(raw: string) {
  const l = parseLink(raw);
  if (l?.to === "session") void router.navigate({ to: "/session/$id", params: { id: l.id } });
  if (l?.to === "repo") void router.navigate({ to: "/repo/$name", params: { name: l.name } });
  if (l?.to === "new") useUI.getState().openNewSession(l.repo, { branch: l.branch, prompt: l.prompt });
}

export function useDeepLinks() {
  useEffect(() => {
    if (!inTauri) return;
    void getCurrent().then((urls) => {
      for (const u of urls ?? []) followLink(u);
    });
    const off = onOpenUrl((urls) => {
      for (const u of urls) followLink(u);
    });
    return () => void off.then((f) => f());
  }, []);
}
