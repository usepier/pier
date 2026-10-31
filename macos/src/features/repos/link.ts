import { open } from "@tauri-apps/plugin-dialog";
import { rpc } from "@/api/client";
import { inTauri } from "@/api/transport";
import { toast, toastError } from "@/state/toasts";

/** Asks for a local clone and links it, so sessions can be created from it. Returns the linked path. */
export async function linkCheckout(hint?: string): Promise<string | undefined> {
  let path: string | null;
  if (inTauri) {
    const picked = await open({
      directory: true,
      multiple: false,
      title: hint ? `Locate your clone of ${hint}` : "Link a repository",
    });
    path = typeof picked === "string" ? picked : null;
  } else {
    path = window.prompt(hint ? `Path to your clone of ${hint}` : "Path to a local clone");
  }
  if (!path) return undefined;
  try {
    const co = await rpc.call("repos.link", { path });
    if (hint && co.name !== hint) {
      toast(`Linked ${co.name} — sessions of ${hint} need a clone in a folder named ${hint}`, "info");
    } else {
      toast(`Linked ${co.name}`, "ok");
    }
    return co.path;
  } catch (e) {
    toastError(e);
    return undefined;
  }
}
