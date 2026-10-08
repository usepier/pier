import { describe, expect, it, vi } from "vitest";

vi.mock("./router", () => ({ router: { navigate: vi.fn() } }));
vi.mock("@tauri-apps/plugin-deep-link", () => ({ getCurrent: vi.fn(), onOpenUrl: vi.fn() }));

const { parseLink } = await import("./deepLinks");

describe("pier:// links", () => {
  it("reads sessions, repositories and new sessions", () => {
    expect(parseLink("pier://session/i-0abc")).toEqual({ to: "session", id: "i-0abc" });
    expect(parseLink("pier://repo/my%20app")).toEqual({ to: "repo", name: "my app" });
    expect(parseLink("pier://new?repo=shop&branch=fix&prompt=fix%20it")).toEqual({
      to: "new",
      repo: "shop",
      branch: "fix",
      prompt: "fix it",
    });
  });
  it("ignores what it doesn't know", () => {
    expect(parseLink("https://example.com")).toBeUndefined();
    expect(parseLink("pier://session")).toBeUndefined();
    expect(parseLink("pier://elsewhere/x")).toBeUndefined();
  });
});
