import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/api/client", () => ({ rpc: {} }));

const { forgetGone, keepFor, useKnown } = await import("./forget");
const { useDrafts } = await import("./drafts");
const { useDocs } = await import("./docs");
const { useBuffers } = await import("./buffers");
const { useOutbox } = await import("./outbox");

describe("forgetGone", () => {
  beforeEach(() => {
    useKnown.setState({ at: {} });
    useDrafts.setState({ drafts: { "i-1/c1": "hello", "i-2/c1": "old" } });
    useDocs.setState({
      docs: { "i-2": [{ id: "diff:a", kind: "diff", path: "a" }] },
      active: { "i-2": "diff:a" },
    });
    useBuffers.setState({ buffers: { "i-2\na.go": { text: "x", baseSha: "" } } });
    useOutbox.setState({
      items: {
        p1: { id: "p1", sessionId: "i-2", chatId: "c1", text: "hi", mode: "queue", at: 0, state: "sending" },
      },
    });
  });

  it("keeps a missing session's things for a month, then forgets them", () => {
    const t0 = Date.UTC(2026, 9, 1);
    forgetGone(["i-1"], t0);
    expect(useDrafts.getState().drafts["i-2/c1"]).toBe("old");
    // i-1 stays listed; i-2 never comes back.
    forgetGone(["i-1"], t0 + keepFor - 1);
    expect(useDocs.getState().docs["i-2"]).toBeDefined();
    forgetGone(["i-1"], t0 + keepFor + 3600e3 + 1);
    expect(useDrafts.getState().drafts).toEqual({ "i-1/c1": "hello" });
    expect(useDocs.getState().docs).toEqual({});
    expect(useDocs.getState().active).toEqual({});
    expect(useBuffers.getState().buffers).toEqual({});
    expect(useOutbox.getState().items).toEqual({});
    expect(Object.keys(useKnown.getState().at)).toEqual(["i-1"]);
  });

  it("keeps a session that comes back within the month", () => {
    const t0 = Date.UTC(2026, 9, 1);
    forgetGone([], t0);
    forgetGone(["i-2"], t0 + keepFor / 2);
    forgetGone([], t0 + keepFor + 1);
    expect(useDrafts.getState().drafts["i-2/c1"]).toBe("old");
  });
});
