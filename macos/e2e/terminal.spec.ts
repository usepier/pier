import { existsSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { expect, type Page, test } from "@playwright/test";

// What a terminal tab sends its program, as the program reads it: a script
// in the mock's shell takes the mouse the way tmux does and keeps every byte
// that comes in, until ^D.
const READER = `import os, sys, tty
tty.setraw(0)
sys.stdout.write("\\x1b[?1002h\\x1b[?1006h")
sys.stdout.flush()
out = open(sys.argv[1], "ab", 0)
while True:
    b = os.read(0, 1024)
    out.write(b)
    if b"\\x04" in b:
        break
sys.stdout.write("\\x1b[?1002l\\x1b[?1006l")
`;

async function openTerminal(page: Page) {
  await page.goto("/#/session/i-01");
  await page.getByRole("button", { name: "New chat or terminal" }).click();
  await page.getByRole("menuitem", { name: "New terminal" }).click();
  await expect(page.getByRole("tab", { name: "zsh" })).toHaveAttribute("aria-selected", "true");
  const canvas = page.locator("canvas").last();
  await expect(canvas).toBeVisible();
  return canvas;
}

test("keys, text, the mouse and the wheel reach the program; ⌘ is left to the menu bar", async ({ page }) => {
  const dir = mkdtempSync(join(tmpdir(), "pier-term-"));
  const script = join(dir, "reader.py");
  const got = join(dir, "in");
  writeFileSync(script, READER);

  const canvas = await openTerminal(page);
  await canvas.click();
  await page.keyboard.type(`python3 ${script} ${got}`);
  await page.keyboard.press("Enter");
  await expect.poll(() => existsSync(got)).toBe(true);
  // Let the program's mouse request land before the mouse moves.
  await page.waitForTimeout(300);

  await page.keyboard.type("hi");
  await page.keyboard.insertText("ü🚢"); // text with no key: the emoji picker
  await page.keyboard.press("Alt+b"); // ⌥ is Meta: ESC b
  const box = (await canvas.boundingBox())!;
  await page.mouse.move(box.x + 30, box.y + 30);
  await page.mouse.wheel(0, -200);
  await page.mouse.click(box.x + 30, box.y + 30);

  // ⌘K goes by: nothing to the program, its default left for the menu bar —
  // as seen just outside the terminal — and a key can still be cancelled
  // after (⌘R's is).
  await canvas.evaluate((c) => {
    const seen: string[] = [];
    (window as unknown as { seen: string[] }).seen = seen;
    c.parentElement!.parentElement!.addEventListener("keydown", (e) => {
      if (!e.metaKey || e.key === "Meta") return;
      seen.push(`${e.key}:${e.defaultPrevented}`);
      if (e.key === "r") {
        e.preventDefault();
        seen.push(`r:${e.defaultPrevented}`);
      }
    });
  });
  await page.keyboard.press("Meta+k");
  await page.keyboard.press("Escape"); // the palette ⌘K opened, in a browser
  await canvas.click({ modifiers: ["Shift"] }); // back, with nothing reported
  await page.keyboard.press("Meta+r");
  expect(await page.evaluate(() => (window as unknown as { seen: string[] }).seen)).toEqual([
    "k:false",
    "r:false",
    "r:true",
  ]);

  await page.keyboard.press("Control+d");
  await expect.poll(() => readFileSync(got, "latin1")).toContain("\x04");
  const bytes = readFileSync(got, "latin1");
  expect(bytes).toContain("hi");
  expect(Buffer.from(bytes, "latin1").toString()).toContain("ü🚢");
  expect(bytes).toContain("\x1bb");
  expect(bytes).toMatch(/\x1b\[<64;\d+;\d+M/); // wheel up, reported
  expect(bytes).toMatch(/\x1b\[<0;\d+;\d+M\x1b\[<0;\d+;\d+m/); // a click: press, release
  expect(bytes).not.toContain("k");
});

test("Copy takes the terminal's selection", async ({ page }) => {
  const canvas = await openTerminal(page);
  await canvas.click();
  await page.keyboard.type("echo copyme");
  await page.keyboard.press("Enter");
  const box = (await canvas.boundingBox())!;
  await page.mouse.move(box.x + 2, box.y + 4);
  await page.mouse.down();
  await page.mouse.move(box.x + box.width - 20, box.y + 60, { steps: 5 });
  await page.mouse.up();
  const copied = await canvas.evaluate((c) => {
    const data = new DataTransfer();
    c.parentElement!.dispatchEvent(new ClipboardEvent("copy", { clipboardData: data, bubbles: true }));
    return data.getData("text/plain");
  });
  expect(copied).toContain("echo copyme\ncopyme");
});
