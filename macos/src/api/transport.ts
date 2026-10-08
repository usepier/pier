// How the app reaches `pier api`: the bundled sidecar inside the app, or —
// in development, or in a plain browser — the dev server's WebSocket bridge
// to the pier built from this checkout.
import { Child, Command } from "@tauri-apps/plugin-shell";

export interface Transport {
  /** Starts the connection. onLine gets each protocol line; onExit fires once. */
  start(onLine: (line: string) => void, onExit: (reason: string) => void): Promise<void>;
  send(line: string): Promise<void>;
  stop(): Promise<void>;
}

export const inTauri = typeof window !== "undefined" && "__TAURI_INTERNALS__" in window;

/** The sidecar transport: `binaries/pier api`, line-delimited JSON on stdio. */
export class SidecarTransport implements Transport {
  private child: Child | null = null;
  private readonly stderrTail: string[] = [];

  constructor(private readonly clientName: string) {}

  async start(onLine: (line: string) => void, onExit: (reason: string) => void) {
    // A web view reload leaves the previous child running: end it first.
    const stale = sessionStorage.getItem("pier.api.pid");
    if (stale) {
      try {
        await new Child(Number(stale)).kill();
      } catch {
        // already gone
      }
    }
    const cmd = Command.sidecar("binaries/pier", ["api", `--client=${this.clientName}`]);
    cmd.stdout.on("data", (line) => {
      const l = line.replace(/\r?\n$/, "");
      if (l) onLine(l);
    });
    cmd.stderr.on("data", (line) => {
      this.stderrTail.push(line);
      if (this.stderrTail.length > 500) this.stderrTail.shift();
    });
    cmd.on("close", (e) => {
      sessionStorage.removeItem("pier.api.pid");
      onExit(`pier api exited (${e.code ?? e.signal})`);
    });
    cmd.on("error", (e) => onExit(`pier api failed: ${e}`));
    this.child = await cmd.spawn();
    sessionStorage.setItem("pier.api.pid", String(this.child.pid));
  }

  async send(line: string) {
    await this.child?.write(`${line}\n`);
  }

  async stop() {
    await this.child?.kill();
    this.child = null;
  }

  /** The last lines pier api wrote to stderr, for diagnostics. */
  log(): string[] {
    return [...this.stderrTail];
  }
}

/** The development bridge (dev/api-bridge.ts). */
export class WebSocketTransport implements Transport {
  private ws: WebSocket | null = null;

  async start(onLine: (line: string) => void, onExit: (reason: string) => void) {
    const url = `${location.protocol === "https:" ? "wss" : "ws"}://${location.host}/__pier_api`;
    const ws = new WebSocket(url);
    this.ws = ws;
    await new Promise<void>((resolve, reject) => {
      ws.onopen = () => resolve();
      ws.onerror = () => reject(new Error("could not reach the dev bridge"));
    });
    ws.onmessage = (e) => onLine(String(e.data));
    ws.onclose = (e) => onExit(e.reason || "the dev bridge closed");
  }

  async send(line: string) {
    this.ws?.send(line);
  }

  async stop() {
    this.ws?.close();
    this.ws = null;
  }
}
