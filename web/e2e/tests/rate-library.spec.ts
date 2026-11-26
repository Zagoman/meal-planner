import { test, expect, type Browser, type Page } from "@playwright/test";

// Seeded by webfake.Seed (test data only).
const PASSWORD = "correct horse";

async function login(browser: Browser, email: string): Promise<Page> {
  const ctx = await browser.newContext({ serviceWorkers: "block" });
  // The entry splash covers the page for 2.5 s; splash.spec.ts tests it.
  await ctx.addInitScript(() => { try { sessionStorage.setItem("splash", "1"); } catch {} });
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
  const cookedTitle = await cards.nth(0).locator(".meal-title").innerText();
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
  await anna.getByRole("link", { name: "Review plan" }).click();
  await expect(anna.getByRole("heading", { name: "Next week", exact: true })).toBeVisible();
  const notCooked = anna.locator("#not-cooked");
  await expect(notCooked).toContainText(notCookedTitle);
  await expect(anna.locator("#draft-meals")).not.toContainText(notCookedTitle);
  await notCooked.getByRole("button", { name: "Add back" }).click();
  await expect(anna.locator("#draft-meals")).toContainText(notCookedTitle, { timeout: 2_000 });
  await expect(anna.locator("#not-cooked")).not.toContainText(notCookedTitle);

  // Library: only the cooked meals are in it. Search, open a recipe, tweak
  // it, add the tweak to this week.
  await anna.goto("/recipes");
  await expect(anna.locator("#recipe-results")).toContainText(cookedTitle);
  await expect(anna.locator("#recipe-results")).not.toContainText(notCookedTitle);
  await expect(anna.locator("#recipe-results")).not.toContainText("Planned");
  const first = cookedTitle.split(" ")[0];
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

test("the plan screen rates the week, then offers what wasn't cooked, then the draft", async ({ browser }) => {
  const anna = await login(browser, "anna@example.test");
  await anna.getByRole("button", { name: "Make next week's plan now" }).click();
  await expect(anna.locator("#draft-card")).toHaveAttribute("data-state", "ready", { timeout: 6_000 });
  await anna.getByRole("link", { name: "Review plan" }).click();
  await expect(anna.getByRole("heading", { name: "Next week", exact: true })).toBeVisible();

  // Rating first, the draft after it.
  const rate = anna.locator("#rate");
  await expect(rate.getByRole("heading", { name: "How was this week?" })).toBeVisible();
  const rateBox = await rate.boundingBox();
  const draftBox = await anna.locator("#draft-meals").boundingBox();
  expect(rateBox!.y).toBeLessThan(draftBox!.y);

  // Not cooked moves the meal under "Not cooked last week" at once (a meal
  // the draft doesn't already have).
  const draftTitles = await anna.locator("#draft-meals .meal-title").allInnerTexts();
  const cards = rate.locator("article");
  let i = 0;
  while (draftTitles.includes(await cards.nth(i).locator(".meal-title").innerText())) i++;
  const card = cards.nth(i);
  const title = await card.locator(".meal-title").innerText();
  await card.getByRole("button", { name: "Not cooked" }).click();
  await expect(anna.locator("#not-cooked")).toContainText(title, { timeout: 2_000 });
  await expect(anna.getByRole("button", { name: "Confirm plan" })).toBeVisible();
  await expect(anna.getByText(/Confirms itself on \w+ at \d\d:\d\d/)).toBeVisible();
});

test("save a planned meal to recipes from its page", async ({ browser }) => {
  const anna = await login(browser, "anna@example.test");
  await anna.goto("/recipes");
  await expect(anna.locator("#recipe-results")).toContainText("No recipes found");
  await anna.goto("/week");
  const meal = anna.locator("a[href^='/meals/']").first();
  await meal.click();
  const title = await anna.getByRole("heading", { level: 1 }).innerText();
  await anna.getByRole("button", { name: "Save to recipes" }).click();
  await expect(anna.locator("#save-recipe")).toContainText("In your recipes");
  await anna.goto("/recipes");
  await expect(anna.locator("#recipe-results")).toContainText(title);
});
