// Development only: serves `pier api` to the web view over a WebSocket, so
// the UI can run in a plain browser or in `tauri dev` against the pier built
// from this checkout. Each connection rebuilds pier (cheap when nothing
// changed) and spawns a fresh `pier api`, so a Go change takes effect on
// reload. Never part of a build.
import { type ChildProcess, spawn, spawnSync } from "node:child_process";
import { resolve } from "node:path";
import type { Plugin } from "vite";
import { WebSocketServer } from "ws";

const repo = resolve(import.meta.dirname, "../..");

export function pierApiBridge(): Plugin {
  return {
    name: "pier-api-bridge",
    apply: "serve",
    configureServer(server) {
      const wss = new WebSocketServer({ noServer: true });
      server.httpServer?.on("upgrade", (req, socket, head) => {
        if (req.url !== "/__pier_api") return;
        wss.handleUpgrade(req, socket, head, (ws) => wss.emit("connection", ws));
      });
      wss.on("connection", (ws) => {
        const built = spawnSync("make", ["-C", repo, "build"], { encoding: "utf8" });
        if (built.status !== 0) {
          ws.close(4000, `pier failed to build:\n${built.stderr || built.stdout}`.slice(0, 120));
          server.config.logger.error(built.stderr || built.stdout);
          return;
        }
        const child: ChildProcess = spawn(resolve(repo, "pier"), ["api"], {
          stdio: ["pipe", "pipe", "pipe"],
        });
        let buf = "";
        child.stdout?.setEncoding("utf8");
        child.stdout?.on("data", (chunk: string) => {
          buf += chunk;
          for (let i = buf.indexOf("\n"); i >= 0; i = buf.indexOf("\n")) {
            const line = buf.slice(0, i);
            buf = buf.slice(i + 1);
            if (line) ws.send(line);
          }
        });
        child.stderr?.setEncoding("utf8");
        child.stderr?.on("data", (chunk: string) => process.stderr.write(chunk));
        child.on("exit", (code) => ws.close(4001, `pier api exited (${code})`));
        ws.on("message", (data) => child.stdin?.write(`${data.toString()}\n`));
        ws.on("close", () => child.stdin?.end());
      });
    },
  };
}
