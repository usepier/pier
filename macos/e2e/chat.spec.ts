import { expect, type Page, test } from "@playwright/test";

async function openSession(page: Page, id: string) {
  await page.goto(`/#/session/${id}`);
  await expect(page.getByRole("tablist").first()).toBeVisible();
}

async function send(page: Page, text: string) {
  const box = page.getByRole("textbox", { name: "Message" });
  await box.fill(text);
  await box.press("Enter");
}

test("a session's chats are tabs, each with its conversation", async ({ page }) => {
  await openSession(page, "i-01");
  const tabs = page.getByRole("tab");
  await expect(tabs.filter({ hasText: "Create hello.txt and add add()" })).toBeVisible();
  await expect(tabs.filter({ hasText: "Plan add() with a docstring" })).toBeVisible();
  await expect(tabs.filter({ hasText: "Ask, plan, then add mul()" })).toBeVisible();
  await expect(page.getByText("Created 1 file, read 1 file, ran 1 command, edited 1 file")).toBeVisible();
  await tabs.filter({ hasText: "Ask, plan, then add mul()" }).click();
  await expect(page.getByText("Added `mul(a, b)`", { exact: false }).or(page.getByText("Added mul(a, b)", { exact: false }))).toBeVisible();
});

test("a turn streams in, asks before running a command, and finishes", async ({ page }) => {
  await openSession(page, "i-01");
  await send(page, "please run the tests");
  await expect(page.getByText("please run the tests")).toBeVisible();
  const card = page.getByRole("alertdialog");
  await expect(card).toContainText("Claude wants to run a command");
  await expect(card).toContainText("pnpm test checkout");
  await card.getByRole("button", { name: "Allow", exact: true }).click();
  await expect(card).toBeHidden();
  await expect(page.getByText("Done.", { exact: false })).toBeVisible();
});

test("a denied command takes a reason with it", async ({ page }) => {
  await openSession(page, "i-01");
  await page.getByRole("tab").filter({ hasText: "Plan add() with a docstring" }).click();
  await page.getByRole("button", { name: "Permissions" }).click();
  await page.getByRole("option", { name: /Ask/ }).click();
  await send(page, "delete the cache");
  const card = page.getByRole("alertdialog");
  await expect(card).toContainText("rm -rf node_modules/.cache");
  await card.getByRole("button", { name: "Deny…" }).click();
  await card.getByRole("textbox", { name: "Why not" }).fill("keep the cache");
  await card.getByRole("button", { name: "Deny", exact: true }).click();
  await expect(page.getByText("Understood — keep the cache")).toBeVisible();
});

test("questions are answered in the app", async ({ page }) => {
  await openSession(page, "i-01");
  await send(page, "any question?");
  const card = page.getByRole("dialog", { name: "Questions" });
  await expect(card).toContainText("Which approach should I take?");
  await card.getByRole("button", { name: /Idempotency key/ }).click();
  await card.getByRole("button", { name: /Unit/ }).click();
  await card.getByRole("button", { name: /Integration/ }).click();
  await card.getByRole("button", { name: "Answer", exact: true }).click();
  await expect(page.getByText("Idempotency key", { exact: false }).last()).toBeVisible();
  await expect(page.getByText("Unit, Integration", { exact: false })).toBeVisible();
});

test("a new Codex chat plans first, and goes on once the plan is approved", async ({ page }) => {
  await openSession(page, "i-02");
  await page.getByRole("button", { name: "New chat or terminal" }).click();
  await page.getByRole("menuitem", { name: "New Codex chat" }).click();
  await expect(page.getByText("New chat with Codex")).toBeVisible();
  await page.getByRole("button", { name: "Permissions" }).click();
  await page.getByRole("option", { name: /Plan/ }).click();
  await send(page, "make checkout charge once");
  const plan = page.getByRole("dialog", { name: "Plan" });
  await expect(plan).toContainText("Fix double charges");
  await plan.getByRole("button", { name: "Approve with full access" }).click();
  await expect(page.getByText("Plan approved — implementing it now.")).toBeVisible();
  await expect(page.getByRole("tab").filter({ hasText: "make checkout charge once" })).toBeVisible();
});

test("a message sent mid-turn queues, and can be taken back", async ({ page }) => {
  await openSession(page, "i-01");
  await page.getByRole("tab").filter({ hasText: "Ask, plan, then add mul()" }).click();
  await page.getByRole("button", { name: "Permissions" }).click();
  await page.getByRole("option", { name: /Auto/ }).click();
  await send(page, "please run the tests");
  await expect(page.getByRole("alertdialog")).toBeVisible();
  const box = page.getByRole("textbox", { name: "Message" });
  await box.fill("then open a PR");
  await box.press("Meta+Shift+Enter");
  await expect(page.getByText("queued — goes in when this turn ends")).toBeVisible();
  await page.getByText("then open a PR").hover();
  await page.getByRole("button", { name: "Remove from the queue" }).click();
  await expect(page.getByText("queued — goes in when this turn ends")).toBeHidden();
});
