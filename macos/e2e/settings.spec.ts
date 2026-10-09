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

test("review comments go to the agent under the user's own heading", async ({ page }) => {
  await page.goto("/#/settings");
  await page.getByRole("button", { name: "Prompts" }).click();
  const box = page.getByRole("textbox", { name: "Review comments prompt", exact: true });
  await box.fill("Please look at these:");
  await box.blur();
  await expect(page.getByText("Saved")).toBeVisible();
  await page.goto("/#/session/i-01");
  await page.getByRole("tab", { name: "Files", exact: true }).click();
  await page.getByRole("button", { name: "src", exact: true }).click();
  await page.getByRole("button", { name: "checkout", exact: true }).click();
  await page.getByRole("button", { name: "PayButton.tsx" }).click();
  await page.getByText("return null;").click();
  await page.getByRole("button", { name: "Comment", exact: true }).click();
  await page.getByPlaceholder("What should change here?").fill("Return the button");
  await page.getByPlaceholder("What should change here?").press("Meta+Enter");
  await page.getByRole("button", { name: "Send 1 to agent" }).click();
  await expect(page.getByText("Please look at these:", { exact: false })).toBeVisible();
});
