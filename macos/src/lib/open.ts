// Handing things to the Mac: a link to the browser, a folder to Finder.
import { openUrl, revealItemInDir } from "@tauri-apps/plugin-opener";
import { inTauri } from "@/api/transport";

export function openExternal(url: string) {
  if (inTauri) void openUrl(url);
  else window.open(url, "_blank", "noopener");
}

/**
 * A web view has no windows to open: what asks for one with window.open (a
 * ⌘-click on a link in a terminal) gets the browser instead.
 */
export function openLinksOutside() {
  if (!inTauri) return;
  window.open = (url) => {
    if (url) void openUrl(String(url));
    return null;
  };
}

export function showInFinder(path: string) {
  if (inTauri) void revealItemInDir(path);
}

/** A repo's web page, from its origin ("host/owner/name"); none for a repo known only by name. */
export function originUrl(origin: string | undefined): string | undefined {
  return origin && /^[^/:]+\/[^/]+\/[^/]+$/.test(origin) ? `https://${origin}` : undefined;
}

/** A path with the home folder as ~. */
export function tildePath(path: string) {
  return path.replace(/^\/Users\/[^/]+(?=\/|$)/, "~");
}
