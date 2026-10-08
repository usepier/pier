import { beforeEach, describe, expect, it, vi } from "vitest";

const handlers = new Map<string, (p: unknown) => void>();
const notes: { method: string; params: unknown }[] = [];
vi.mock("./client", () => ({
  rpc: {
    on: (m: string, h: (p: unknown) => void) => handlers.set(m, h),
    notify: (method: string, params: unknown) => notes.push({ method, params }),
  },
}));

const { attachSink, sendInput } = await import("./terminal");

const b64 = (s: string) => btoa(s);

describe("terminal plumbing", () => {
  beforeEach(() => {
    notes.length = 0;
  });

  it("holds output that beats the open reply, then delivers it in order", () => {
    handlers.get("term.output")!({ termId: "t1", seq: 1, data: b64("hello ") });
    handlers.get("term.output")!({ termId: "t1", seq: 2, data: b64("world") });
    const got: string[] = [];
    attachSink("t1", {
      output: (bytes, done) => (got.push(new TextDecoder().decode(bytes)), done()),
      exit: () => {},
    });
    expect(got.join("")).toBe("hello world");
  });

  it("acknowledges rendered output in batches", () => {
    const chunk = "x".repeat(20 * 1024);
    attachSink("t2", { output: (_b, done) => done(), exit: () => {} });
    handlers.get("term.output")!({ termId: "t2", seq: 1, data: b64(chunk) });
    expect(notes.filter((n) => n.method === "term.ack")).toHaveLength(0);
    handlers.get("term.output")!({ termId: "t2", seq: 2, data: b64(chunk) });
    const acks = notes.filter((n) => n.method === "term.ack");
    expect(acks).toHaveLength(1);
    expect((acks[0]!.params as { bytes: number }).bytes).toBe(40 * 1024);
  });

  it("splits big pastes", () => {
    sendInput("t3", "y".repeat(10_000));
    const inputs = notes.filter((n) => n.method === "term.input");
    expect(inputs).toHaveLength(3);
  });
});
