import { test, expect, type Browser, type Page } from "@playwright/test";

// Seeded by webfake.Seed (test data only). The e2e server simulates Claude
// jobs with a 2 s delay.
const PASSWORD = "correct horse";

async function login(browser: Browser, email: string): Promise<Page> {
  const ctx = await browser.newContext({ serviceWorkers: "block" });
  const page = await ctx.newPage();
  await page.goto("/week");
  await expect(page).toHaveURL(/\/login/);
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

test("two phones review a draft together and confirm once", async ({ browser }) => {
  const anna = await login(browser, "anna@example.test");
  const ben = await login(browser, "ben@example.test");

  // Anna makes the draft; both phones' cards go to "making", then "ready",
  // without reloading.
  await anna.getByRole("button", { name: "Make next week's plan now" }).click();
  for (const p of [anna, ben]) {
    await expect(p.locator("#draft-card")).toHaveAttribute("data-state", "making", { timeout: 2_000 });
  }
  for (const p of [anna, ben]) {
    await expect(p.locator("#draft-card")).toHaveAttribute("data-state", "ready", { timeout: 6_000 });
    await expect(p.getByText("Next week is ready")).toBeVisible();
  }

  // Both open the review (full screen: no tab bar). "Start review" opens
  // the rating step first (rate-library.spec.ts); go straight to the draft.
  for (const p of [anna, ben]) {
    await p.goto("/plans/next");
    await expect(p.getByRole("heading", { name: "Next week", exact: true })).toBeVisible();
    await expect(p.locator("nav.tabs")).toHaveCount(0);
  }
  const first = (p: Page) => p.locator("#draft-meals article").first();
  const title = await first(anna).locator(".meal-title").innerText();

  // Anna's thumbs-down shows on Ben's phone within 2 s.
  await first(anna).getByRole("button", { name: "Thumbs down" }).click();
  await expect(first(anna).getByRole("button", { name: "Thumbs down" })).toHaveAttribute("aria-pressed", "true");
  await expect(first(ben)).toContainText("disliked by Anna", { timeout: 2_000 });
  await expect(first(ben)).toHaveClass(/disliked/);

  // Ben swaps it: both see "Swapping…" within 2 s, then a new meal in the
  // same place.
  await first(ben).getByRole("button", { name: `Swap ${title}` }).click();
  for (const p of [anna, ben]) {
    await expect(first(p)).toContainText("Swapping", { timeout: 2_000 });
  }
  for (const p of [anna, ben]) {
    await expect(first(p).locator(".meal-title")).not.toHaveText(title, { timeout: 6_000 });
    await expect(first(p)).not.toContainText("Swapping");
    await expect(first(p)).not.toHaveClass(/disliked/);
  }

  // Remove the last meal on Anna's phone: gone on Ben's too.
  const count = await anna.locator("#draft-meals article").count();
  await anna.locator("#draft-meals article").last().getByRole("button", { name: /^Remove / }).click();
  await expect(ben.locator("#draft-meals article")).toHaveCount(count - 1, { timeout: 2_000 });

  // Both phones confirm at the same moment, sent with each phone's session
  // (a click can miss: the first confirm already sends the other phone to
  // the week). Both are answered with the week; the plan confirms once.
  const confirm = (p: Page) => p.request.post("/plans/next/confirm", { maxRedirects: 0 });
  const answers = await Promise.all([confirm(anna), confirm(ben)]);
  for (const res of answers) {
    expect(res.status()).toBe(303);
    expect(res.headers()["location"]).toBe("/week");
  }
  for (const p of [anna, ben]) {
    await expect(p).toHaveURL(/\/week$/, { timeout: 3_000 }); // sent there over SSE
  }
  for (const p of [anna, ben]) {
    await expect(p).toHaveURL(/\/week$/);
    await expect(p.locator("#draft-card")).toHaveAttribute("data-state", "none");
    await expect(p.locator("#week-meals article")).toHaveCount(count - 1);
  }
});

test("a draft confirmed on one phone sends the other back to the week", async ({ browser }) => {
  const anna = await login(browser, "anna@example.test");
  const ben = await login(browser, "ben@example.test");
  await anna.getByRole("button", { name: "Make next week's plan now" }).click();
  await expect(anna.locator("#draft-card")).toHaveAttribute("data-state", "ready", { timeout: 6_000 });
  await ben.goto("/plans/next");
  await expect(ben.getByRole("heading", { name: "Next week", exact: true })).toBeVisible();

  await anna.goto("/plans/next");
  await anna.getByRole("button", { name: "Confirm week" }).click();
  await expect(anna).toHaveURL(/\/week$/);
  await expect(ben).toHaveURL(/\/week$/, { timeout: 3_000 });
});
