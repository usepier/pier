import { expect, test } from "@playwright/test";

test("settings: sections in a sidebar, rows with their controls, a search across them", async ({ page }) => {
  await page.goto("/#/settings");
  await expect(page.getByRole("heading", { name: "General" })).toBeVisible();
  await page.getByRole("button", { name: "Follow-up behavior" }).click();
  await page.getByRole("option", { name: "Queue" }).click();
  await expect(page.getByRole("button", { name: "Follow-up behavior" })).toContainText("Queue");

  await page.getByRole("button", { name: "Agents", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Agents" })).toBeVisible();
  await page.getByRole("button", { name: "Claude permissions" }).click();
  await page.getByRole("option", { name: /plan/ }).click();
  await expect(page.getByText("Saved")).toBeVisible();

  await page.getByRole("button", { name: "shop", exact: true }).click();
  await expect(page.getByRole("heading", { name: "shop" })).toBeVisible();
  await expect(page.getByText("Pool size")).toBeVisible();

  await page.getByRole("textbox", { name: "Search settings" }).fill("park after");
  await expect(page.getByRole("heading", { name: "Idle & cost" })).toBeVisible();
  await expect(page.getByText("Park after")).toBeVisible();
  await page.getByRole("button", { name: "Back" }).click();
  await expect(page).not.toHaveURL(/settings/);
});
