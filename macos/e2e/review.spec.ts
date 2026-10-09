import { expect, type Page, test } from "@playwright/test";

async function openSession(page: Page, id: string) {
  await page.goto(`/#/session/${id}`);
  await expect(page.getByRole("tablist").first()).toBeVisible();
}

async function inspector(page: Page, name: string) {
  await page.getByRole("tab", { name, exact: true }).click();
}

test("changes follow the agent's edits as the session pushes them", async ({ page }) => {
  await openSession(page, "i-01");
  await inspector(page, "Changes");
  const pay = page.getByRole("button", { name: "M src/checkout/PayButton.tsx" });
  await expect(pay).toContainText("+4");
  const box = page.getByRole("textbox", { name: "Message" });
  await box.fill("make the pay button safe");
  await box.press("Enter");
  // The turn edits PayButton.tsx: its count moves without a refresh.
  await expect(pay).toContainText("+6");

  // Its diff, then the whole file around the change.
  await pay.click();
  await expect(page.getByText("edit 2", { exact: false })).toBeVisible();
  await page.getByRole("button", { name: "Whole file" }).click();
  await expect(page.getByText("line 1", { exact: false })).toBeVisible();
  await page.getByRole("button", { name: "Changes only" }).click();
  await expect(page.getByText("line 1", { exact: false })).toBeHidden();
});

test("search streams matches in as they're found", async ({ page }) => {
  await openSession(page, "i-01");
  await inspector(page, "Files");
  await page.getByRole("button", { name: "Search", exact: true }).click();
  await page.getByRole("textbox", { name: "Search the checkout" }).fill("createcharge");
  await expect(page.getByText("3 matches in 3 files")).toBeVisible();
  await page.getByRole("button", { name: "Match case" }).click();
  await expect(page.getByText("No matches").or(page.getByText("0 matches in 0 files"))).toBeVisible();
});

test("a parked session shows its files as of the last look, read-only", async ({ page }) => {
  await openSession(page, "i-03");
  await inspector(page, "Changes");
  await expect(page.getByText("as of the last look", { exact: false })).toBeVisible();
  await expect(page.getByRole("button", { name: "Commit…" })).toBeHidden();
  await inspector(page, "Files");
  await expect(page.getByText("Parked — as of the last look", { exact: false })).toBeVisible();
  await page.getByRole("button", { name: "src", exact: true }).click();
  await page.getByRole("button", { name: "checkout", exact: true }).click();
  await page.getByRole("button", { name: "PayButton.tsx" }).click();
  await expect(page.getByText("as of the last look · read-only")).toBeVisible();
  await expect(page.getByRole("button", { name: "Save" })).toBeHidden();
  // A file that didn't change wasn't kept.
  await page.getByRole("button", { name: "Summary.tsx" }).click();
  await expect(page.getByText("Only changed files are kept", { exact: false })).toBeVisible();
});

test("a comment on lines of a file goes to the agent with the rest", async ({ page }) => {
  await openSession(page, "i-01");
  await inspector(page, "Files");
  await page.getByRole("button", { name: "src", exact: true }).click();
  await page.getByRole("button", { name: "checkout", exact: true }).click();
  await page.getByRole("button", { name: "PayButton.tsx" }).click();
  await page.getByText("return null;").click();
  await page.getByRole("button", { name: "Comment", exact: true }).click();
  await expect(page.getByText("Line 3")).toBeVisible();
  await page.getByPlaceholder("What should change here?").fill("Return the button");
  await page.getByPlaceholder("What should change here?").press("Meta+Enter");
  await expect(page.locator(".cm-commented")).toHaveCount(1);
  await expect(page.getByText("Return the button")).toBeVisible();
  await page.getByRole("button", { name: "Send 1 to agent" }).click();
  await expect(page.getByText("Sent 1 comment to the agent")).toBeVisible();
  await expect(page.locator(".cm-commented")).toHaveCount(0);
});

test("commands put the focus where they say: a new chat's message box, the search field", async ({ page }) => {
  await openSession(page, "i-01");
  await page.getByRole("tab", { name: "Changes", exact: true }).click();
  await page.keyboard.press("Meta+t");
  await expect(page.getByRole("textbox", { name: "Message" })).toBeFocused();
  await page.keyboard.press("Meta+Shift+f");
  await expect(page.getByRole("textbox", { name: "Search the checkout" })).toBeFocused();
  await page.keyboard.press("Meta+l");
  await expect(page.getByRole("textbox", { name: "Message" })).toBeFocused();
});
