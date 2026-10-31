import AxeBuilder from "@axe-core/playwright";
import { expect, type Page, test } from "@playwright/test";

// Every main view, checked by axe for what keeps assistive technology
// (VoiceOver) from working: missing names, roles, contrast, structure.

async function audit(page: Page, what: string) {
  const r = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"])
    // The code views color tokens by theme; their contrast is the theme's.
    .exclude(".shiki")
    .exclude("diffs-container")
    .analyze();
  const serious = r.violations.filter((v) => v.impact === "serious" || v.impact === "critical");
  const report = serious.map(
    (v) =>
      `${v.id} (${v.impact}): ${v.help}\n  ${v.nodes
        .slice(0, 3)
        .map((n) => `${n.html.slice(0, 160)}\n    ${(n.failureSummary ?? "").replace(/\n/g, " ")}`)
        .join("\n  ")}`,
  );
  expect(report, `${what}:\n${report.join("\n")}`).toEqual([]);
}

test("home", async ({ page }) => {
  await page.goto("/#/");
  await expect(page.getByText("Needs you")).toBeVisible();
  await audit(page, "home");
});

test("a repository", async ({ page }) => {
  await page.goto("/#/repo/shop");
  await expect(page.getByText("Sessions ·", { exact: false })).toBeVisible();
  await audit(page, "repo");
});

test("a session: chat, changes, files", async ({ page }) => {
  await page.goto("/#/session/i-01");
  await expect(page.getByRole("textbox", { name: "Message" })).toBeVisible();
  await audit(page, "chat");
  await page.getByRole("tab", { name: "Files", exact: true }).click();
  await audit(page, "files");
});

test("a waiting request", async ({ page }) => {
  await page.goto("/#/session/i-01");
  const box = page.getByRole("textbox", { name: "Message" });
  await box.fill("please run the tests");
  await box.press("Enter");
  await expect(page.getByRole("alertdialog")).toBeVisible();
  await audit(page, "permission card");
});

test("settings and setup", async ({ page }) => {
  await page.goto("/#/settings");
  await expect(page.getByRole("heading", { name: "General" })).toBeVisible();
  await audit(page, "settings");
  await page.goto("/#/setup");
  await expect(page.getByText("Set up pier")).toBeVisible();
  await audit(page, "setup");
});
