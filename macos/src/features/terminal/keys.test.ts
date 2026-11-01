import { describe, expect, it } from "vitest";
import { optionAsMeta } from "./keys";

const key = (code: string, mods: Partial<KeyboardEvent> = {}) =>
  ({ code, altKey: true, ctrlKey: false, metaKey: false, shiftKey: false, ...mods }) as KeyboardEvent;

describe("optionAsMeta", () => {
  it("sends ESC and the key as it reads without ⌥", () => {
    expect(optionAsMeta(key("KeyB"))).toBe("\x1bb");
    expect(optionAsMeta(key("KeyB", { shiftKey: true }))).toBe("\x1bB");
    expect(optionAsMeta(key("Digit3"))).toBe("\x1b3");
    expect(optionAsMeta(key("Period"))).toBe("\x1b.");
    expect(optionAsMeta(key("Comma", { shiftKey: true }))).toBe("\x1b<");
    expect(optionAsMeta(key("Backspace"))).toBe("\x1b\x7f");
  });

  it("leaves everything else to the terminal", () => {
    expect(optionAsMeta(key("KeyB", { altKey: false }))).toBeUndefined();
    expect(optionAsMeta(key("KeyB", { ctrlKey: true }))).toBeUndefined();
    expect(optionAsMeta(key("KeyB", { metaKey: true }))).toBeUndefined();
    expect(optionAsMeta(key("ArrowLeft"))).toBeUndefined();
    expect(optionAsMeta(key("Digit3", { shiftKey: true }))).toBeUndefined();
  });
});
