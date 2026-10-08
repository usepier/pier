import { ask } from "@tauri-apps/plugin-dialog";
import { inTauri } from "@/api/transport";

/** A native confirmation alert; resolves true when confirmed. */
export async function confirm(
  title: string,
  message: string,
  opts: { ok?: string; destructive?: boolean } = {},
): Promise<boolean> {
  if (!inTauri) return window.confirm(`${title}\n\n${message}`);
  return ask(message, {
    title,
    kind: opts.destructive ? "warning" : "info",
    okLabel: opts.ok ?? "OK",
    cancelLabel: "Cancel",
  });
}
