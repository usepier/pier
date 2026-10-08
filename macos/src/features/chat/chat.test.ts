import { describe, expect, it } from "vitest";
import type { ChatItem } from "@/api/protocol.gen";
import { applyChange, emptyChat, fromSnapshot, prependPage } from "@/state/chat";
import { groupItems, summarize } from "./group";
import { insertMention, mentionAt, rank } from "./mentions";

const t = "2026-10-08T10:00:00Z";
const user = (id: string, text = "hi"): ChatItem => ({ id, kind: "user", time: t, text });
const tool = (id: string, view = "bash", status = "ok"): ChatItem => ({
  id,
  kind: "tool",
  time: t,
  tool: { id, name: "Bash", view, status, summary: "ls" } as ChatItem["tool"],
});

describe("chat changes", () => {
  it("appends, updates, resets and tracks the agent", () => {
    let st = fromSnapshot({ items: [user("a"), tool("b", "bash", "running")], more: true, live: true });
    st = applyChange(st, { op: "update", items: [tool("b")] });
    expect(st.items[1]!.tool!.status).toBe("ok");
    st = applyChange(st, { op: "append", items: [tool("b"), user("c")] });
    expect(st.items.map((i) => i.id)).toEqual(["a", "b", "c"]);
    st = applyChange(st, { op: "agent", live: false, asOf: t });
    expect([st.live, st.asOf]).toEqual([false, t]);
    st = applyChange(st, { op: "reset", items: [user("z")], more: false });
    expect([st.items.length, st.more]).toEqual([1, false]);
  });
  it("puts older pages in front once", () => {
    const st = prependPage({ ...emptyChat, items: [user("c")] }, [user("a"), user("b"), user("c")], false);
    expect(st.items.map((i) => i.id)).toEqual(["a", "b", "c"]);
  });
});

describe("grouping", () => {
  it("folds finished runs of steps between messages", () => {
    const blocks = groupItems([user("u"), tool("1"), tool("2", "edit"), tool("3"), user("v")]);
    expect(blocks.map((b) => b.kind)).toEqual(["item", "steps", "item"]);
    expect(summarize((blocks[1] as { items: ChatItem[] }).items)).toBe("Ran 2 commands, edited 1 file");
  });
  it("keeps the run in progress open, and short runs as they are", () => {
    expect(
      groupItems([tool("1"), tool("2"), tool("3", "bash", "running")]).every((b) => b.kind === "item"),
    ).toBe(true);
    expect(groupItems([tool("1"), tool("2"), user("u")]).map((b) => b.kind)).toEqual([
      "item",
      "item",
      "item",
    ]);
  });
});

describe("mentions", () => {
  it("finds the @-query at the caret and replaces it", () => {
    const text = "look at @src/ma please";
    const at = mentionAt(text, 15)!;
    expect(at).toEqual({ start: 8, query: "src/ma" });
    expect(insertMention(text, at, "src/main.go")).toEqual({
      text: "look at @src/main.go  please",
      caret: 21,
    });
    expect(mentionAt("mail@example", 12)).toBeNull();
  });
  it("ranks file names over paths, tight matches over loose", () => {
    const paths = ["docs/main.md", "src/main.go", "src/domain/admin.go", "Makefile"];
    expect(rank(paths, "main")).toEqual(["src/main.go", "docs/main.md", "src/domain/admin.go"]);
    expect(rank(paths, "mkf")).toEqual(["Makefile"]);
  });
});

import { rankEntries } from "@/features/palette/rank";

describe("palette", () => {
  const run = () => {};
  const entries = [
    { id: "s:1", kind: "session" as const, title: "fix-login", detail: "shop · idle", run },
    { id: "r:shop", kind: "repo" as const, title: "shop", detail: "repository", run },
    { id: "c:new", kind: "command" as const, title: "New Session", keywords: "create start", run },
  ];
  it("finds by name, and > lists actions only", () => {
    expect(rankEntries(entries, "login").map((e) => e.id)).toEqual(["s:1"]);
    expect(rankEntries(entries, ">").map((e) => e.id)).toEqual(["c:new"]);
    expect(rankEntries(entries, ">create").map((e) => e.id)).toEqual(["c:new"]);
  });
});
