import { describe, expect, it } from "vitest";
import { age, branchProblem, cleanLog, hourly, monthly } from "./format";

describe("format", () => {
  it("prices like pier quotes them", () => {
    expect(hourly(0.04)).toBe("$0.04/h");
    expect(hourly(0)).toBe("$0/h");
    expect(monthly(3.8)).toBe("$3.80/mo");
    expect(monthly(15.4)).toBe("$15/mo");
  });

  it("ages", () => {
    const now = Date.parse("2026-10-08T12:00:00Z");
    expect(age("2026-10-08T11:00:00Z", now)).toBe("1h");
    expect(age("2026-10-05T12:00:00Z", now)).toBe("3d");
    expect(age(undefined, now)).toBe("");
  });

  it("accepts the branch names a create accepts", () => {
    expect(branchProblem("feat/login-2")).toBeNull();
    expect(branchProblem("-x")).toMatch(/start/);
    expect(branchProblem("a..b")).toMatch(/\.\./);
    expect(branchProblem("a b")).toMatch(/only/);
    expect(branchProblem("x.lock")).toMatch(/end/);
  });

  it("cleans logs the way a terminal would show them", () => {
    expect(cleanLog("\x1b[32mok\x1b[0m")).toBe("ok");
    expect(cleanLog("10%\r50%\r100%")).toBe("100%");
  });
});
