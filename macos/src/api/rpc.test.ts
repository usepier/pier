import { describe, expect, it, vi } from "vitest";
import { Disconnected, Rpc, RpcError } from "./rpc";
import type { Transport } from "./transport";

/** A transport whose server side the test plays. */
class FakeTransport implements Transport {
  sent: Record<string, unknown>[] = [];
  onLine: (l: string) => void = () => {};
  onExit: (r: string) => void = () => {};
  async start(onLine: (l: string) => void, onExit: (r: string) => void) {
    this.onLine = onLine;
    this.onExit = onExit;
  }
  async send(line: string) {
    const m = JSON.parse(line);
    this.sent.push(m);
    if (m.method === "initialize")
      this.reply(m.id, { protocol: 1, version: "t", configured: true, cloud: "AWS" });
  }
  async stop() {}
  reply(id: number, result: unknown) {
    this.onLine(JSON.stringify({ jsonrpc: "2.0", id, result }));
  }
  push(method: string, params: unknown) {
    this.onLine(JSON.stringify({ jsonrpc: "2.0", method, params }));
  }
  last(method: string) {
    return [...this.sent].reverse().find((m) => m.method === method) as { id: number; params: unknown };
  }
}

async function connected() {
  const t = new FakeTransport();
  const rpc = new Rpc(() => t, { name: "test", version: "0" });
  await rpc.start();
  return { t, rpc };
}

describe("Rpc", () => {
  it("initializes, then answers calls by id", async () => {
    const { t, rpc } = await connected();
    expect(rpc.status).toBe("ready");
    const p = rpc.call("settings.get", {});
    t.reply(t.last("settings.get").id, { groups: [], settings: [] });
    await expect(p).resolves.toEqual({ groups: [], settings: [] });
  });

  it("turns error replies into RpcErrors with their kind", async () => {
    const { t, rpc } = await connected();
    const p = rpc.call("sessions.resume", { id: "i-1" });
    const { id } = t.last("sessions.resume");
    t.onLine(
      JSON.stringify({
        jsonrpc: "2.0",
        id,
        error: {
          code: -32000,
          message: "expired",
          data: { kind: "auth_expired", message: "expired", login: ["aws", "login"] },
        },
      }),
    );
    const err = (await p.catch((e) => e)) as RpcError;
    expect(err).toBeInstanceOf(RpcError);
    expect(err.kind).toBe("auth_expired");
    expect(err.login).toEqual(["aws", "login"]);
  });

  it("routes progress to its call and sends a cancel on abort", async () => {
    const { t, rpc } = await connected();
    const onProgress = vi.fn();
    const ac = new AbortController();
    const p = rpc.call("pool.drain", { repo: "app" }, { onProgress, signal: ac.signal });
    const { id } = t.last("pool.drain");
    t.push("$/progress", { id, event: { kind: "step", message: "draining", elapsedMs: 5 } });
    expect(onProgress).toHaveBeenCalledWith({ kind: "step", message: "draining", elapsedMs: 5 });
    ac.abort();
    expect(t.last("$/cancelRequest").params).toEqual({ id });
    t.reply(id, { removed: 0 });
    await p;
  });

  it("fails pending calls when the connection drops", async () => {
    const { t, rpc } = await connected();
    vi.useFakeTimers();
    const p = rpc.call("doctor.run", {});
    t.onExit("killed");
    await expect(p).rejects.toBeInstanceOf(Disconnected);
    expect(rpc.status).toBe("restarting");
    vi.useRealTimers();
  });

  it("delivers subscription events in order and resubscribes after a gap", async () => {
    const { t, rpc } = await connected();
    const seen: unknown[] = [];
    rpc.watch("overview", {}, (d) => seen.push(d));
    await Promise.resolve();
    const first = t.last("overview.watch");
    t.reply(first.id, { sub: "overview-1", seq: 0, snapshot: "snap" });
    await new Promise((r) => setTimeout(r, 0));
    t.push("sub.event", { sub: "overview-1", seq: 1, topic: "overview", data: "one" });
    t.push("sub.event", { sub: "overview-1", seq: 3, topic: "overview", data: "skipped" });
    expect(seen).toEqual(["snap", "one"]);
    await Promise.resolve();
    const again = t.last("overview.watch");
    expect(again.id).not.toBe(first.id);
  });
});
