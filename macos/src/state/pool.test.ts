import { describe, expect, it } from "vitest";
import type { Repo } from "@/api/protocol.gen";
import { poolState } from "./pool";

const repo = (p: Partial<Repo>): Repo => ({
  name: "shop",
  hasPier: true,
  imageKind: "",
  baking: false,
  needsImage: false,
  imageMonthlyUsd: 0,
  poolTarget: 1,
  poolReady: 0,
  poolFilling: 0,
  poolStale: 0,
  poolAges: [],
  sessions: 0,
  monthlyUsd: 0,
  hourlyUsd: 0,
  idleMonthlyUsd: 0,
  reminders: [],
  checkout: { path: "/code/shop", name: "shop", missing: false },
  ...p,
});

describe("pool state", () => {
  it("says why a pool isn't ready", () => {
    expect(poolState(repo({ poolReady: 1 }), false, false).kind).toBe("ready");
    expect(poolState(repo({ poolFilling: 1 }), false, false).kind).toBe("filling");
    expect(poolState(repo({}), false, true).kind).toBe("filling");
    const off = poolState(repo({}), false, false);
    expect([off.kind, off.short]).toEqual(["short", "pool 0/1 · empty"]);
    expect(off.detail).toContain("Not refilling on its own");
    expect(poolState(repo({}), true, false).short).toBe("pool 0/1 · refilling soon");
    expect(poolState(repo({ checkout: undefined }), true, false).short).toBe("pool 0/1 · no clone");
    expect(poolState(repo({ poolTarget: 0 }), false, false).kind).toBe("none");
  });
});

describe("a repo that needs its image", () => {
  it("says the pool waits for it, and offers no fill", () => {
    const st = poolState(repo({ needsImage: true }), false, false);
    expect(st.kind).toBe("none");
    expect(st.detail).toContain("Waits for the session image");
  });
});

describe("a pool whose last fill failed", () => {
  it("says why, unless a fill is on its way", () => {
    const failed = repo({
      poolError: { at: new Date().toISOString(), message: ".pier/setup.sh failed (exit 2)" },
    });
    expect(poolState(failed, false, false).kind).toBe("failed");
    expect(poolState(failed, false, false).detail).toContain(".pier/setup.sh failed (exit 2)");
    expect(poolState(failed, false, true).kind).toBe("filling");
  });
});
