import { expect, test } from "@playwright/test";

test("a session's trouble is an icon in the sidebar, and its words show on hover", async ({ page }) => {
  // Seen its finished turn first: that would come before the pull request.
  await page.goto("/#/session/i-02");
  await expect(page.getByRole("tablist").first()).toBeVisible();
  await page.goto("/#/");
  const row = page.getByRole("button", { name: /^Session fix-login/ });
  await expect(row).toHaveAccessibleName(/CI is failing on #41/);
  await expect(row).not.toContainText("CI failing");
  await row.locator("svg").last().hover();
  await expect(page.getByRole("tooltip")).toHaveText("CI is failing on #41");
});

test("a repo that needs its image says so, and nothing starts without it", async ({ page }) => {
  await page.goto("/#/repo/infra");
  await expect(page.getByRole("button", { name: /^Repository infra: Needs its session image/ })).toBeVisible();
  await expect(page.getByText("infra needs its session image")).toBeVisible();
  await expect(page.getByRole("button", { name: "Bake now" })).toBeEnabled();
  await expect(page.getByRole("button", { name: "New Session", exact: true })).toBeDisabled();
  await expect(page.getByRole("button", { name: "Fill now" })).toBeDisabled();
  await expect(page.getByText("Waits for the session image")).toBeVisible();
});

test("the session's own shell isn't a tab until something runs in it", async ({ page }) => {
  await page.goto("/#/session/i-01");
  const strip = page.getByRole("tablist", { name: "Chats, terminals and files" });
  await expect(strip.getByRole("tab", { name: "server" })).toBeVisible();
  await expect(strip.getByRole("tab", { name: "bash" })).toHaveCount(0);
  await expect(strip.getByRole("tab", { name: "Shell" })).toHaveCount(0);
});

test("the toolbar's repo, branch and folder are links", async ({ page }) => {
  await page.goto("/#/session/i-02");
  await page.getByRole("button", { name: "shop", exact: true }).click();
  await expect(page).toHaveURL(/#\/repo\/shop$/);
  await expect(page.getByRole("button", { name: "acme/shop" })).toBeVisible();
  await expect(page.getByRole("button", { name: "~/code/shop" })).toBeVisible();
});

test("no field corrects, capitalizes or suggests what's typed", async ({ page }) => {
  await page.goto("/#/session/i-01");
  const box = page.getByRole("textbox", { name: "Message" });
  await expect(box).toHaveAttribute("autocorrect", "off");
  await expect(box).toHaveAttribute("spellcheck", "false");
  await page.keyboard.press("Meta+k");
  const palette = page.getByRole("dialog", { name: "Go to or run" }).getByRole("textbox", { name: "Search" });
  await expect(palette).toHaveAttribute("autocapitalize", "off");
});

test("New Session is a message box: one picker for model, effort and speed; Enter starts it and it opens", async ({
  page,
}) => {
  await page.goto("/#/repo/shop");
  await page.getByRole("button", { name: "New Session", exact: true }).click();
  await expect(page).toHaveURL(/#\/new/);
  // From origin's default branch unless changed; the branch has no field:
  // the first prompt names it.
  await expect(page.getByRole("button", { name: "origin/main" })).toBeVisible();
  await expect(page.getByRole("button", { name: "named after the prompt" })).toHaveCount(0);
  // One picker: every provider's models by name and version, the default as
  // the model it is; effort and fast with them.
  const picker = page.getByRole("button", { name: "Model" });
  await expect(picker).toContainText("Opus 5.5"); // the agent setting's model
  // 1M context is a switch on a model that has the variant, not a model.
  await picker.click();
  await page.locator("label").filter({ has: page.getByRole("switch", { name: "1M context" }) }).click();
  await page.keyboard.press("Escape");
  await expect(picker).toContainText("1M");
  await picker.click();
  await expect(page.getByRole("option", { name: /^Claude default/ })).toHaveCount(0);
  // One default: the model new chats run, not each provider's.
  await expect(page.getByRole("listbox", { name: "Models" }).getByText("default", { exact: true })).toHaveCount(1);
  await expect(page.getByRole("option", { name: /Opus 5.5.*default/ })).toBeVisible();
  await page.getByRole("textbox", { name: "Search models" }).fill("6.1");
  await page.getByRole("option", { name: /GPT-6.1 Sol/ }).click();
  await picker.click();
  await page.getByRole("button", { name: /^Effort/ }).click();
  await page.getByRole("dialog", { name: "Effort" }).getByRole("button", { name: "Ultra" }).click();
  await expect(picker).toContainText("GPT-6.1 Sol");
  await expect(picker).toContainText("Ultra");
  await picker.click();
  await page.locator("label").filter({ has: page.getByRole("switch", { name: "Fast" }) }).click();
  await expect(page.getByRole("switch", { name: "Fast" })).toBeChecked();
  await page.keyboard.press("Escape");
  await page.getByRole("textbox", { name: "First prompt" }).fill("fix the login redirect");
  await page.keyboard.press("Enter");
  await expect(page).toHaveURL(/#\/session\//);
  await expect(page.getByRole("heading", { name: "oslo" })).toBeVisible();
});

test("the default model is one setting, picked like a chat's model", async ({ page }) => {
  await page.goto("/#/settings");
  await page.getByRole("button", { name: "Agents" }).click();
  await expect(page.getByText("Claude model")).toHaveCount(0);
  const picker = page.getByRole("button", { name: "Model" });
  await expect(picker).toContainText("Opus 5.5");
  await picker.click();
  await expect(page.getByRole("switch", { name: "Fast" })).toHaveCount(0);
  await page.getByRole("option", { name: /GPT-6.1 Sol/ }).click();
  await expect(picker).toContainText("GPT-6.1 Sol");
  await picker.click();
  await expect(page.getByRole("option", { name: /GPT-6.1 Sol.*default/ })).toBeVisible();
  await expect(page.getByRole("listbox", { name: "Models" }).getByText("default", { exact: true })).toHaveCount(1);
  // Back as it was, for the tests after.
  await page.getByRole("option", { name: /Opus 5.5/ }).click();
  await expect(picker).toContainText("Opus 5.5");
});

test("a pool whose last fill failed says why, and offers to fill again", async ({ page }) => {
  await page.goto("/#/repo/website");
  await expect(page.getByRole("button", { name: /^Repository website: The pool's last fill failed/ })).toBeVisible();
  await expect(page.getByText(".pier/setup.sh failed (exit 2)", { exact: false }).first()).toBeVisible();
  await page.getByRole("button", { name: "Show log" }).click();
  await expect(page.getByRole("dialog", { name: "website's pool log" })).toContainText("setup.sh failed (exit 2)");
  await page.keyboard.press("Escape");
  await expect(page.getByRole("button", { name: "Fill again" })).toBeEnabled();
  await expect(page.getByText("the session image", { exact: true })).toBeVisible();
  // No job logs in the cards.
  await expect(page.getByText("waiting for .pier/setup.sh")).toHaveCount(0);
});

test("the message box keeps its keys to the buttons' tooltips", async ({ page }) => {
  await page.goto("/#/session/i-01");
  const box = page.getByRole("textbox", { name: "Message" });
  await expect(page.getByText("steers this turn")).toHaveCount(0);
  expect((await box.boundingBox())!.height).toBeGreaterThan(70);
  await box.fill("one more thing");
  // (Tooltips open on hover and on keyboard focus; here, the keyboard's.)
  await page.getByRole("button", { name: "Attach files" }).focus();
  await page.keyboard.press("Tab");
  await expect(page.getByRole("button", { name: "Send" })).toBeFocused();
  await expect(page.getByRole("tooltip")).toContainText(/Steer|Queue|Send/);
});

test("a pull request not looked for yet says so, rather than that there is none", async ({ page }) => {
  await page.goto("/#/session/i-06");
  await page.getByRole("tab", { name: "PR", exact: true }).click();
  await expect(page.getByText("Checking GitHub…")).toBeVisible();
  await expect(page.getByText(/No pull request/)).toHaveCount(0);
});

test("a confirmation is the app's own modal: Escape cancels, Return goes ahead", async ({ page }) => {
  page.once("dialog", () => {
    throw new Error("a system dialog opened");
  });
  await page.goto("/#/session/i-02");
  await page.getByRole("button", { name: "Remove", exact: true }).click();
  const modal = page.getByRole("alertdialog", { name: "Remove fix-login?" });
  await expect(modal).toBeVisible();
  await expect(modal.getByRole("button", { name: "Remove" })).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(modal).toHaveCount(0);
  await expect(page).toHaveURL(/#\/session\/i-02/); // nothing removed
});

test("pull request comments and reviews read as GitHub shows them, with nothing that runs", async ({ page }) => {
  page.on("dialog", () => {
    throw new Error("something in a review ran");
  });
  await page.goto("/#/session/i-02");
  await page.getByRole("tab", { name: "PR #41" }).click();
  const review = page.getByText("one thing about the cookie name").locator("..");
  await expect(review.locator("strong", { hasText: "Close" })).toBeVisible();
  await expect(page.getByText("<details>")).toHaveCount(0);
  await page.locator("summary", { hasText: "Details" }).click();
  await expect(page.getByRole("cell", { name: "hard-coded cookie" })).toBeVisible();
  await expect(page.locator("script", { hasText: "alert" })).toHaveCount(0);
  await expect(page.locator("img[onerror]")).toHaveCount(0);
  await expect(page.locator("code", { hasText: "config" })).toBeVisible();
});

test("a merged session cleans up from the sidebar", async ({ page }) => {
  await page.goto("/#/repo/website");
  const row = page.getByRole("button", { name: /^Session old-banner/ });
  // (Its label changes as it goes: the name stays.)
  await expect(row).toBeVisible();
  await row.hover();
  await page.getByRole("button", { name: "Clean up old-banner" }).hover();
  await expect(page.getByText("#12 merged - Clean up VM & disk")).toBeVisible();
  await page.getByRole("button", { name: "Clean up old-banner" }).click();
  const modal = page.getByRole("alertdialog", { name: "Clean up old-banner?" });
  await modal.getByRole("button", { name: "Clean up" }).click();
  // At once, while the VM goes: the row says so, with no clean-up to press again.
  await expect(page.getByRole("button", { name: "Clean up old-banner" })).toHaveCount(0, { timeout: 300 });
  await expect(page.getByRole("button", { name: "Session old-banner, Cleaning up…" })).toBeVisible({ timeout: 300 });
  await expect(page.getByText("Cleaned up old-banner")).toBeVisible();
  await expect(row).toHaveCount(0);
  // Others have no such button.
  await expect(page.getByRole("button", { name: "Clean up fix-login" })).toHaveCount(0);
});

test("an action that takes a while says so on its button, and can't be pressed twice", async ({ page }) => {
  await page.goto("/#/session/i-02");
  await page.getByRole("tab", { name: "PR #41" }).click();
  const fix = page.getByRole("button", { name: "Fix CI…" });
  await fix.click();
  await expect(fix).toHaveAttribute("data-pending", "true", { timeout: 300 });
  await expect(page.getByText("In the message box — read it over and send")).toBeVisible();
  await expect(fix).not.toHaveAttribute("data-pending", "true");
});

test("a new session starts from origin's branch unless the clone is asked for", async ({ page }) => {
  const creates: Record<string, unknown>[] = [];
  page.on("websocket", (ws) =>
    ws.on("framesent", (f) => {
      const m = JSON.parse(String(f.payload));
      if (m.method === "sessions.create") creates.push(m.params);
    }),
  );
  const start = async (pickLocal: boolean) => {
    await page.goto("/#/repo/shop");
    await page.getByRole("button", { name: "New Session", exact: true }).click();
    if (pickLocal) {
      await page.getByRole("button", { name: "origin/main" }).click();
      await page.getByRole("button", { name: /\(local\)/ }).click();
      await page.keyboard.press("Escape");
    }
    await page.getByRole("textbox", { name: "First prompt" }).fill("look around");
    await page.keyboard.press("Enter");
    await expect.poll(() => creates.length).toBeGreaterThan(pickLocal ? 1 : 0);
  };
  await start(false);
  expect(creates[0]!.base).toBeUndefined(); // pier's default: origin's branch, fetched
  await start(true);
  expect(creates[1]!.base).toBe("HEAD");
});

test("a bake in progress reads as its stages and the step it's on, its log on request", async ({ page }) => {
  await page.goto("/#/");
  const job = page.getByRole("region", { name: "Baking api" });
  const stages = job.getByRole("list", { name: "Stages" });
  await expect(stages.getByRole("listitem")).toHaveCount(4);
  await expect(stages.locator('[aria-current="step"]')).toHaveText("Repo setup");
  await expect(job).toContainText("Running .pier/setup.sh to completion");
  await expect(job.getByText("Checking out api")).toBeHidden();
  await job.getByRole("button", { name: "Every step (6)" }).click();
  await expect(job.getByText("Checking out api")).toBeVisible();
});

test("a bake started outside the app is in progress too", async ({ page }) => {
  await page.goto("/#/");
  await expect(page.getByRole("region", { name: "Baking docs" })).toContainText("Started outside the app");
  // One the app follows isn't shown twice.
  await expect(page.getByRole("region", { name: /^Baking api/ })).toHaveCount(1);
});

test("a job whose end the live updates missed still ends, from pier's own list", async ({ page }) => {
  await page.clock.install();
  await page.goto("/#/");
  const card = page.getByRole("region", { name: "Filling the pool of docs" });
  await expect(card).toBeVisible();
  await page.clock.runFor(21_000);
  await expect(card).toHaveCount(0);
});
