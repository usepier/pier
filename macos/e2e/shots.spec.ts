import { expect, test } from "@playwright/test";

// Not a test: screenshots for a look (PIER_SHOTS=1).
test.skip(!process.env.PIER_SHOTS, "screenshots only on request");

test("shots", async ({ page }) => {
  await page.goto("/#/session/i-01");
  const box = page.getByRole("textbox", { name: "Message" });
  await box.fill("please run the tests");
  await box.press("Enter");
  await page.waitForTimeout(700);
  await page.screenshot({ path: "/tmp/shot-streaming.png" });
  await expect(page.getByRole("alertdialog")).toBeVisible();
  await page.screenshot({ path: "/tmp/shot-permission.png" });
  await page.getByRole("button", { name: "Allow", exact: true }).click();
  await expect(page.getByText("Done.", { exact: false })).toBeVisible();
  await page.screenshot({ path: "/tmp/shot-done.png" });
  await box.fill("any question?");
  await box.press("Enter");
  await expect(page.getByRole("dialog", { name: "Questions" })).toBeVisible();
  await page.screenshot({ path: "/tmp/shot-question.png" });
  await page.getByRole("button", { name: "Skip" }).click();
  await page.getByRole("tab").filter({ hasText: "Plan add()" }).click();
  await page.getByRole("textbox", { name: "Message" }).fill("plan the fix");
  await page.getByRole("textbox", { name: "Message" }).press("Enter");
  await expect(page.getByRole("dialog", { name: "Plan" })).toBeVisible();
  await page.screenshot({ path: "/tmp/shot-plan.png" });
  await page.getByRole("button", { name: "Permissions" }).click();
  await page.screenshot({ path: "/tmp/shot-picker.png" });
});

test("settings shots", async ({ page }) => {
  await page.goto("/#/settings");
  await expect(page.getByRole("heading", { name: "General" })).toBeVisible();
  await page.screenshot({ path: "/tmp/shot-settings.png" });
  await page.getByRole("button", { name: "Agents" }).click();
  await page.screenshot({ path: "/tmp/shot-settings-agents.png" });
  await page.getByRole("button", { name: "shop" }).click();
  await page.screenshot({ path: "/tmp/shot-settings-repo.png" });
  await page.getByRole("textbox", { name: "Search settings" }).fill("park");
  await page.screenshot({ path: "/tmp/shot-settings-search.png" });
});
