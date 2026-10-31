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

test("steps stay one block as they come, and what's opened stays open", async ({ page }) => {
  await openSession(page, "i-01");
  await send(page, "please run the tests");
  const card = page.getByRole("alertdialog");
  await expect(card).toContainText("pnpm test checkout");
  // The search done and the command waiting: one block, saying what runs.
  const block = page.getByRole("button", { name: /Ran\s+pnpm test checkout\s+· 2 steps/ });
  await expect(block).toBeVisible();
  await block.click();
  await expect(block).toHaveAttribute("aria-expanded", "true");
  await card.getByRole("button", { name: "Allow", exact: true }).click();
  await expect(page.getByText("Done.", { exact: false })).toBeVisible();
  // More steps joined and the turn ended: still the one block, still open.
  const done = page.getByRole("button", { name: "Ran 1 search, ran 1 command, edited 1 file" });
  await expect(done).toHaveAttribute("aria-expanded", "true");
  await expect(page.getByRole("button", { name: /Edited\s+src\/checkout\/PayButton\.tsx/ })).toBeVisible();
});

test("the working time ticks each second", async ({ page }) => {
  // The page's clock is the test's: three seconds pass at once, with no
  // other update (a new line, a status) to redraw the line.
  await page.clock.install();
  await openSession(page, "i-01");
  await send(page, "think it over slowly");
  const elapsed = page.getByText(/^\d+s$/);
  await expect(elapsed).toBeVisible();
  const first = Number((await elapsed.textContent())!.slice(0, -1));
  await page.clock.pauseAt(Date.now() + 100);
  await page.clock.runFor(3000);
  expect(Number((await elapsed.textContent())!.slice(0, -1))).toBeGreaterThanOrEqual(first + 3);
});

test("a new terminal shows at once and stays shown; closing one is instant", async ({ page }) => {
  await openSession(page, "i-01");
  await page.getByRole("button", { name: "New chat or terminal" }).click();
  await page.getByRole("menuitem", { name: "New terminal" }).click();
  // Before the session has made it: the tab, picked, opening.
  const tab = page.getByRole("tab", { name: "Terminal" });
  await expect(tab).toHaveAttribute("aria-selected", "true", { timeout: 300 });
  await expect(page.getByText("Opening a terminal…")).toBeVisible();
  // Made: the window's tab, still picked — before the session's next look.
  const made = page.getByRole("tab", { name: "zsh" });
  await expect(made).toHaveAttribute("aria-selected", "true", { timeout: 2000 });
  await page.waitForTimeout(3500); // the session's look comes, and agrees
  await expect(made).toHaveAttribute("aria-selected", "true");
  // Closing: gone at once, the tab before it shown.
  await made.press("Delete");
  await expect(made).toHaveCount(0, { timeout: 300 });
  await page.waitForTimeout(3500);
  await expect(page.getByRole("tab", { name: "zsh" })).toHaveCount(0);
});

test("a / at the start lists the agent's commands, and picking one puts it in", async ({ page }) => {
  await openSession(page, "i-01");
  const box = page.getByRole("textbox", { name: "Message" });
  await box.fill("/");
  const menu = page.getByRole("listbox", { name: "Commands" });
  await expect(menu.getByRole("option")).toHaveCount(4);
  await box.pressSequentially("dep");
  await expect(menu.getByRole("option")).toHaveCount(1);
  await expect(menu).toContainText("/deploy-preview");
  await box.press("Enter");
  await expect(box).toHaveValue("/deploy-preview ");
  await expect(menu).toBeHidden();
  // Past the command, it's a message: Return sends it.
  await box.pressSequentially("to staging");
  await box.press("Enter");
  await expect(page.getByText("/deploy-preview to staging")).toBeVisible();
});

test("the permission mode picked last is where new chats start; plan mode isn't kept", async ({ page }) => {
  await openSession(page, "i-01");
  const pick = async (name: RegExp) => {
    await page.getByRole("button", { name: "Permissions" }).click();
    await page.getByRole("option", { name }).click();
  };
  await pick(/Bypass/);
  await page.getByRole("button", { name: "New chat or terminal" }).click();
  await page.getByRole("menuitem", { name: "New Claude chat" }).click();
  await expect(page.getByRole("button", { name: "Permissions" })).toContainText("Bypass");
  await pick(/Plan/);
  await page.getByRole("button", { name: "New chat or terminal" }).click();
  await page.getByRole("menuitem", { name: "New Claude chat" }).click();
  await expect(page.getByRole("button", { name: "Permissions" })).toContainText("Bypass");
  await pick(/Ask/); // as it was, for the tests after
});
