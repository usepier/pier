// The local proxy (sessions as <session>.pier, their ports on this Mac) and
// opening a session's port in the browser.
import { openUrl } from "@tauri-apps/plugin-opener";
import { rpc } from "@/api/client";
import { useWatch } from "@/api/live";
import type { ProxyState, Session } from "@/api/protocol.gen";
import { RpcError } from "@/api/rpc";
import { inTauri } from "@/api/transport";
import { attempt, toast } from "./toasts";

let latest: ProxyState | undefined;

export function useProxy(): ProxyState | undefined {
  const st = useWatch("proxy", {}, { graceMs: 600_000 }).data;
  latest = st;
  return st;
}

export async function startProxy() {
  try {
    await rpc.call("proxy.start", {});
  } catch (e) {
    if (e instanceof RpcError && e.kind === "cancelled") return;
    toast(`The proxy didn't start: ${(e as Error).message}`, "bad");
  }
}

export async function stopProxy() {
  await attempt(() => rpc.call("proxy.stop", {}));
}

function open(url: string) {
  if (inTauri) void openUrl(url);
  else window.open(url, "_blank", "noopener");
}

/**
 * Opens a session's port: at <session>.pier while the proxy serves it,
 * else forwarded to the same port on localhost.
 */
export async function openPort(s: Session, port: number) {
  const p = latest?.running ? latest.sessions.find((x) => x.id === s.id) : undefined;
  if (p?.ports.includes(port)) return open(`http://${p.host}:${port}`);
  if (p?.localhost.includes(port)) return open(`http://localhost:${port}`);
  const forwarded = await rpc.call("ports.list", {}).catch(() => []);
  if (!forwarded.some((f) => f.sessionId === s.id && f.remote === port)) {
    const ok = await attempt(() => rpc.call("ports.forward", { sessionId: s.id, local: port, remote: port }));
    if (ok === undefined) return;
  }
  open(`http://localhost:${port}`);
}
