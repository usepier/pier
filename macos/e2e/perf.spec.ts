import { expect, test } from "@playwright/test";

// The app with a lot of everything (PIER_MOCK_BIG): it stays quick.

test("57 sessions list and open without a wait", async ({ page }) => {
  const t0 = Date.now();
  await page.goto("/#/");
  await expect(page.getByText("branch-49")).toBeVisible();
  const listed = Date.now() - t0;
  expect(listed).toBeLessThan(3000);
  await page.getByText("branch-49").click();
  await expect(page.getByRole("heading", { name: "branch-49" })).toBeVisible();
  console.log(`listed in ${listed} ms`);
});

test("a 5,000-item chat opens at its end, renders only what shows, and pages back", async ({ page }) => {
  await page.goto("/#/session/i-01");
  await expect(page.getByRole("tablist").first()).toBeVisible();
  const t0 = Date.now();
  await page.getByRole("tab").filter({ hasText: "A very long chat" }).click();
  await expect(page.getByText("The last word of a long chat.")).toBeVisible();
  const opened = Date.now() - t0;
  expect(opened).toBeLessThan(2500);
  // Virtualized: a screenful of rows in the page, not thousands.
  const rows = await page.locator("[data-index]").count();
  expect(rows).toBeLessThan(80);
  // Scrolling up brings older pages in: earlier messages than any the
  // first page held.
  const oldest = async () => {
    const texts = await page.getByText(/^Message \d+:/).allTextContents();
    return Math.min(...texts.map((t) => Number(/Message (\d+)/.exec(t)![1])));
  };
  const scroller = page.locator("[data-index]").first().locator("xpath=ancestor::*[contains(@class,'overflow-y-auto')][1]");
  for (let i = 0; i < 4; i++) {
    await scroller.evaluate((el) => {
      el.scrollTop = 0;
    });
    await page.waitForTimeout(300);
  }
  expect(await oldest()).toBeLessThan(4600);
  console.log(`opened in ${opened} ms, ${rows} rows rendered`);
});

test("typing in the composer of a long chat keeps up", async ({ page }) => {
  await page.goto("/#/session/i-01");
  await page.getByRole("tab").filter({ hasText: "A very long chat" }).click();
  await expect(page.getByText("The last word of a long chat.")).toBeVisible();
  const box = page.getByRole("textbox", { name: "Message" });
  await box.click();
  const text = "a quick note about the next step of the refactor, typed one key at a time";
  const t0 = Date.now();
  await box.pressSequentially(text);
  const typed = Date.now() - t0;
  await expect(box).toHaveValue(text);
  // Playwright's own key events take a few ms each; the app adds little.
  expect(typed / text.length).toBeLessThan(40);
  console.log(`${(typed / text.length).toFixed(1)} ms a key`);
});
