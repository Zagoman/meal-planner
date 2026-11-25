import { test, expect, type Browser, type Page } from "@playwright/test";

// Seeded by webfake.Seed (test data only).
const PASSWORD = "correct horse";

async function login(browser: Browser, email: string): Promise<Page> {
  const ctx = await browser.newContext({ serviceWorkers: "block" });
  const page = await ctx.newPage();
  await page.goto("/week");
  await page.getByText("Sign in with a password").click();
  await page.locator("details").getByLabel("Email").fill(email);
  await page.getByLabel("Password").fill(PASSWORD);
  await page.getByRole("button", { name: "Log in" }).click();
  await expect(page.getByRole("heading", { name: "This week", exact: true })).toBeVisible();
  return page;
}

test.beforeEach(async ({ request }) => {
  expect((await request.post("/__e2e/reset")).status()).toBe(204);
});

test("rate the week, add a not-cooked meal back, tweak and add a recipe", async ({ browser }) => {
  const anna = await login(browser, "anna@example.test");

  // Rate this week first (here before the draft is made, so the not-cooked
  // meal is kept out of it).
  await anna.goto("/plans/current/rate");
  await expect(anna.getByRole("heading", { name: "How was this week?" })).toBeVisible();
  const cards = anna.locator(".rate-list article");
  const n = await cards.count();
  const notCookedTitle = await cards.nth(1).locator(".meal-title").innerText();
  for (let i = 0; i < n; i++) {
    if (i === 1) {
      await cards.nth(i).getByRole("button", { name: "Not cooked" }).click();
      await expect(cards.nth(i)).toContainText("add it back");
    } else {
      await cards.nth(i).getByRole("button", { name: "4 of 5" }).click();
      await expect(cards.nth(i).getByRole("button", { name: "4 of 5" })).toHaveAttribute("aria-pressed", "true");
    }
  }
  await expect(anna.locator("#rate-progress")).toContainText(`${n} of ${n} done`);

  // Make the draft: the not-cooked meal waits under "Not cooked last week".
  await anna.getByRole("link", { name: "Done" }).click();
  await anna.getByRole("button", { name: "Make next week's plan now" }).click();
  await expect(anna.locator("#draft-card")).toHaveAttribute("data-state", "ready", { timeout: 6_000 });
  await anna.getByRole("link", { name: "Start review" }).click(); // all rated: straight to the draft
  await expect(anna.getByRole("heading", { name: "Next week", exact: true })).toBeVisible();
  const notCooked = anna.locator("#not-cooked");
  await expect(notCooked).toContainText(notCookedTitle);
  await expect(anna.locator("#draft-meals")).not.toContainText(notCookedTitle);
  await notCooked.getByRole("button", { name: "Add back" }).click();
  await expect(anna.locator("#draft-meals")).toContainText(notCookedTitle, { timeout: 2_000 });
  await expect(anna.locator("#not-cooked")).not.toContainText(notCookedTitle);

  // Library: search, open a recipe, tweak it, add the tweak to this week.
  await anna.goto("/recipes");
  const first = notCookedTitle.split(" ")[0];
  await anna.getByLabel("Search recipes").fill(first);
  await expect(anna.locator("#recipe-results .rec").first()).toContainText(first, { timeout: 2_000 });
  await anna.locator("#recipe-results .rec").first().click();
  await expect(anna.getByText("Tweak this recipe")).toBeVisible();
  await anna.getByLabel("Ask for a change to this recipe").fill("more vegetables");
  await anna.getByRole("button", { name: "Send" }).click();
  await expect(anna.locator("#tweak")).toContainText("Tweaking", { timeout: 2_000 });
  await expect(anna.locator("#tweak")).toContainText("New version saved", { timeout: 6_000 });
  await anna.locator("#tweak").getByRole("link", { name: "Open it" }).click();
  await expect(anna.getByRole("heading", { name: /\(tweaked\)$/ })).toBeVisible();
  await anna.getByRole("button", { name: "Add to this week" }).click();
  await expect(anna.locator("#flash")).toContainText("this week's list");
});
