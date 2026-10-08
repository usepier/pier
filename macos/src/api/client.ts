// The one connection to pier for the whole app.
import { Rpc } from "./rpc";
import { inTauri, SidecarTransport, type Transport, WebSocketTransport } from "./transport";

const useBridge = !inTauri || import.meta.env.VITE_PIER_API === "bridge";

let sidecar: SidecarTransport | null = null;

export const rpc = new Rpc(
  (): Transport => {
    if (useBridge) return new WebSocketTransport();
    sidecar = new SidecarTransport(`pier-mac/${import.meta.env.VITE_APP_VERSION ?? "dev"}`);
    return sidecar;
  },
  { name: "pier-mac", version: import.meta.env.VITE_APP_VERSION ?? "dev" },
  true,
);

/** pier api's recent stderr, when it runs as the sidecar. */
export function apiLog(): string[] {
  return sidecar?.log() ?? [];
}
